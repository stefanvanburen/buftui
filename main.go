package main

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/bufbuild/httplb"
	"github.com/bufbuild/protovalidate-go"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jdx/go-netrc"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}

func run(_ context.Context) error {
	fs := ff.NewFlagSet("buftui")
	var (
		// `-r` is for reference, which should generally be preferred.
		remoteFlag     = fs.StringLong("remote", "buf.build", "BSR remote")
		fullscreenFlag = fs.Bool('f', "fullscreen", "Enable fullscreen display")
		usernameFlag   = fs.String('u', "username", "", "Set username for authentication (default: login for remote in ~/.netrc)")
		tokenFlag      = fs.String('t', "token", "", "Set token for authentication (default: password for remote in ~/.netrc)")
		referenceFlag  = fs.String('r', "reference", "", "Set BSR reference to open")
	)
	if err := ff.Parse(fs, os.Args[1:]); err != nil {
		fmt.Printf("%s\n", ffhelp.Flags(fs))
		return err
	}

	username := *usernameFlag
	token := *tokenFlag

	parsedRemote, parsedReference, err := parseReference(*referenceFlag)
	if err != nil {
		return fmt.Errorf("parsing reference flag: %w", err)
	}
	if parsedRemote != "" && *remoteFlag != "" && *remoteFlag != parsedRemote {
		return fmt.Errorf("cannot provide conflicting `--remote` flag (%s) and reference remote (%s)", *remoteFlag, parsedRemote)
	}
	// We know the remotes at least aren't conflicting, so take whichever is non-empty.
	remote := cmp.Or(*remoteFlag, parsedRemote)
	// Sanity check for `--remote ""`, or an invalid parsed reference.
	if remote == "" {
		return fmt.Errorf("remote cannot be empty")
	}

	// Either username && token should be provided at the CLI, or they should be loaded from the ~/.netrc.
	if (username == "" && token != "") || (username != "" && token == "") {
		return fmt.Errorf("must set both username and token flags, or neither (and load authentication from ~/.netrc)")
	}
	if username == "" && token == "" {
		var err error
		username, token, err = getUserTokenFromNetrc(remote)
		if err != nil {
			return fmt.Errorf("getting netrc credentials for remote %q: %w", remote, err)
		}
	}
	if username == "" {
		return fmt.Errorf("username must be set, either by flag or in the ~/.netrc file")
	}
	if token == "" {
		return fmt.Errorf("token must be set, either by flag or in the ~/.netrc file")
	}

	listStyles := list.DefaultStyles()
	listStyles.Title = listStyles.Title.Foreground(bufBlue).Background(bufTeal).Bold(true)

	listItemStyles := list.NewDefaultItemStyles()
	listItemStyles.SelectedTitle = listItemStyles.SelectedTitle.Foreground(bufBlue).BorderLeftForeground(bufBlue).Bold(true)
	listItemStyles.SelectedDesc = listItemStyles.SelectedDesc.Foreground(bufBlue).BorderLeftForeground(bufBlue)
	listItemStyles.NormalTitle = listItemStyles.NormalTitle.Foreground(bufBlue)

	httpClient := httplb.NewClient()
	defer httpClient.Close()

	initialState := modelStateSearching
	if parsedReference != nil {
		initialState = modelStateLoadingReference
	}

	model := model{
		state:            initialState,
		currentOwner:     username,
		spinner:          spinner.New(spinner.WithSpinner(spinner.Dot)),
		listStyles:       listStyles,
		listItemStyles:   listItemStyles,
		client:           newClient(httpClient, remote, username, token),
		help:             help.New(),
		keys:             keys,
		currentReference: parsedReference,
		searchInput:      newSearchInput(),
	}

	var options []tea.ProgramOption
	if *fullscreenFlag {
		options = append(options, tea.WithAltScreen())
	}
	if _, err := tea.NewProgram(model, options...).Run(); err != nil {
		return err
	}
	return nil
}

type modelState int

const (
	modelStateBrowsingModules modelState = iota
	modelStateBrowsingCommits
	modelStateBrowsingCommitContents
	modelStateBrowsingCommitFileContents
	modelStateSearching
	modelStateLoadingReference
	modelStateLoadingModules
	modelStateLoadingCommits
	modelStateLoadingCommitFileContents
)

const (
	bufBlue = lipgloss.Color("#151fd5")
	bufTeal = lipgloss.Color("#91dffb")
)

type model struct {
	// (Basically) Static
	listStyles     list.Styles
	listItemStyles list.DefaultItemStyles
	spinner        spinner.Model
	client         *client
	keys           keyMap

	// State - where are we?
	state modelState
	// Should exit when setting this.
	err error

	// State-related data
	currentOwner       string
	currentModule      string
	currentCommit      string
	currentModules     modulesMsg
	currentCommits     commitsMsg
	currentCommitFiles []*modulev1.File
	currentReference   *modulev1.ResourceRef_Name

	// Sub-models
	moduleList      list.Model
	commitList      list.Model
	commitFilesList list.Model
	fileViewport    viewport.Model
	searchInput     textinput.Model
	help            help.Model
}

func (m model) Init() tea.Cmd {
	inits := []tea.Cmd{m.spinner.Tick}
	if m.currentReference != nil {
		inits = append(inits, m.client.getResource(m.currentReference))
	}
	return tea.Batch(inits...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// If we set a width on the help menu it can gracefully truncate
		// its view as needed.
		m.help.Width = msg.Width

	case resourceMsg:
		switch retrievedResource := msg.retrievedResource.Value.(type) {
		case *modulev1.Resource_Module:
			m.currentOwner = msg.requestedResource.Owner
			m.currentModule = retrievedResource.Module.Name
			m.state = modelStateLoadingCommits
			return m, m.client.listCommits(m.currentOwner, m.currentModule)
		case *modulev1.Resource_Commit:
			m.currentOwner = msg.requestedResource.Owner
			m.currentModule = msg.requestedResource.Module
			m.currentCommit = retrievedResource.Commit.Id
			m.state = modelStateLoadingCommitFileContents
			return m, m.client.getCommitContent(m.currentOwner, m.currentModule, m.currentCommit)
		case *modulev1.Resource_Label:
			// TODO: Is this possible? I guess so?
			// We don't handle it right now, though.
			m.err = fmt.Errorf("cannot handle type resource of type %T", retrievedResource)
			return m, tea.Quit
		default:
			m.err = fmt.Errorf("cannot handle type resource of type %T", retrievedResource)
			return m, tea.Quit
		}

	case modulesMsg:
		m.state = modelStateBrowsingModules
		m.currentModules = msg
		if len(m.currentModules) == 0 {
			return m, nil
		}
		modules := make([]list.Item, len(m.currentModules))
		for i, currentModule := range m.currentModules {
			modules[i] = &module{currentModule}
		}
		delegate := list.NewDefaultDelegate()
		delegate.Styles = m.listItemStyles
		// TODO: Show module description.
		delegate.ShowDescription = false
		delegate.SetSpacing(0)
		moduleList := list.New(
			modules,
			delegate,
			100,                     // TODO: Figure out the width of the terminal?
			len(m.currentModules)*4, // TODO: Pick a reasonable value here.
		)
		moduleList.Title = fmt.Sprintf("Modules (Owner: %s)", m.currentOwner)
		moduleList.Styles = m.listStyles
		moduleList.AdditionalFullHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Right}
		}
		moduleList.AdditionalShortHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Right}
		}
		m.moduleList = moduleList
		return m, nil

	case commitsMsg:
		m.state = modelStateBrowsingCommits
		m.currentCommits = msg
		if len(m.currentCommits) == 0 {
			return m, nil
		}
		commits := make([]list.Item, len(m.currentCommits))
		for i, currentCommit := range m.currentCommits {
			commits[i] = &commit{currentCommit}
		}
		delegate := list.NewDefaultDelegate()
		delegate.Styles = m.listItemStyles
		commitList := list.New(
			commits,
			delegate,
			100, // TODO: Figure out the width of the terminal?
			// 5 seems to avoid automatic pagination.
			len(m.currentCommits)*5,
		)
		commitList.Title = fmt.Sprintf("Commits (Module: %s/%s)", m.currentOwner, m.currentModule)
		commitList.Styles = m.listStyles
		commitList.AdditionalFullHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Left, keys.Right}
		}
		commitList.AdditionalShortHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Left, keys.Right}
		}
		m.commitList = commitList
		return m, nil

	case contentsMsg:
		m.state = modelStateBrowsingCommitContents
		m.currentCommitFiles = msg.Files
		viewportHeight := len(msg.Files)
		commitFiles := make([]list.Item, len(m.currentCommitFiles))
		for i, currentCommitFile := range m.currentCommitFiles {
			commitFiles[i] = &commitFile{currentCommitFile}
		}
		delegate := list.NewDefaultDelegate()
		delegate.Styles = m.listItemStyles
		delegate.ShowDescription = false
		delegate.SetSpacing(0)
		commitFilesList := list.New(
			commitFiles,
			delegate,
			100,
			len(commitFiles)+8, // TODO: Pick a reasonable value here.
		)
		commitFilesList.Title = fmt.Sprintf("Commit %s (Module: %s/%s)", m.currentCommit, m.currentOwner, m.currentModule)
		commitFilesList.SetShowStatusBar(false)
		commitFilesList.Styles = m.listStyles
		commitFilesList.AdditionalFullHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Left, keys.Right}
		}
		commitFilesList.AdditionalShortHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Left, keys.Right}
		}
		m.commitFilesList = commitFilesList
		m.fileViewport = viewport.New(100, max(viewportHeight, 30))
		// Set up the initial viewport.
		commitFileItem := m.commitFilesList.SelectedItem()
		commitFile, ok := commitFileItem.(*commitFile)
		if !ok {
			panic("only commit files should be in commit files list")
		}
		selectedFileName := commitFile.underlying.Path
		var fileContents string
		for _, file := range m.currentCommitFiles {
			if file.Path == selectedFileName {
				fileContents = string(file.Content)
				break
			}
		}
		highlightedFile, err := highlightFile(selectedFileName, fileContents)
		if err != nil {
			m.err = fmt.Errorf("can't highlight file: %w", err)
			return m, tea.Quit
		}
		m.fileViewport.SetContent(highlightedFile)
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Help):
			m.help.ShowAll = !m.help.ShowAll
		case key.Matches(msg, m.keys.Search):
			// From anywhere other than the search state, "s"
			// enters a search state.
			if m.state != modelStateSearching {
				// "s" -> "search"
				m.state = modelStateSearching
				m.searchInput = newSearchInput()
				return m, nil
			}
		case key.Matches(msg, m.keys.Enter):
			switch m.state {
			case modelStateSearching:
				m.currentOwner = m.searchInput.Value()
				// TODO: Clear search input?
				return m, m.client.getModules(m.currentOwner)
			}
			// enter or l are equivalent for all the cases below.
			fallthrough
		case key.Matches(msg, m.keys.Right):
			switch m.state {
			case modelStateBrowsingModules:
				if len(m.currentModules) == 0 {
					// Don't do anything.
					return m, nil
				}
				m.state = modelStateLoadingCommits
				item := m.moduleList.SelectedItem()
				module, ok := item.(*module)
				if !ok {
					panic("items in moduleList should be modules")
				}
				m.currentModule = module.underlying.Name
				return m, m.client.listCommits(m.currentOwner, m.currentModule)
			case modelStateBrowsingCommits:
				if len(m.currentCommits) == 0 {
					// Don't do anything.
					return m, nil
				}
				m.state = modelStateLoadingCommitFileContents
				item := m.commitList.SelectedItem()
				commit, ok := item.(*commit)
				if !ok {
					panic("items in commitList should be commits")
				}
				m.currentCommit = commit.underlying.Id
				return m, m.client.getCommitContent(m.currentOwner, m.currentModule, m.currentCommit)
			case modelStateBrowsingCommitContents:
				m.state = modelStateBrowsingCommitFileContents
				return m, nil
			default:
				// Don't do anything, yet, in other states.
			}
		case key.Matches(msg, m.keys.Left):
			// "h" -> "Go out"
			switch m.state {
			case modelStateBrowsingCommitFileContents:
				m.state = modelStateBrowsingCommitContents
				return m, nil
			case modelStateBrowsingCommitContents:
				// NOTE: We don't necessarily have the commits
				// list populated, because we may have gone
				// directly to a reference.
				// TODO: Hook this up to caching.
				m.state = modelStateLoadingCommits
				return m, m.client.listCommits(m.currentOwner, m.currentModule)
			case modelStateBrowsingCommits:
				// NOTE: We don't necessarily have the module
				// list populated, because we may have gone
				// directly to a reference.
				// TODO: Hook this up to caching.
				m.state = modelStateLoadingModules
				return m, m.client.getModules(m.currentOwner)
			}
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	switch m.state {
	case modelStateBrowsingModules:
		m.moduleList, cmd = m.moduleList.Update(msg)
	case modelStateBrowsingCommits:
		m.commitList, cmd = m.commitList.Update(msg)
	case modelStateBrowsingCommitContents:
		m.commitFilesList, cmd = m.commitFilesList.Update(msg)
		item := m.commitFilesList.SelectedItem()
		commitFile, ok := item.(*commitFile)
		if !ok {
			panic("only commit files should be in item list")
		}
		selectedFileName := commitFile.underlying.Path
		var fileContents string
		for _, file := range m.currentCommitFiles {
			if file.Path == selectedFileName {
				fileContents = string(file.Content)
				break
			}
		}
		highlightedFile, err := highlightFile(selectedFileName, fileContents)
		if err != nil {
			m.err = fmt.Errorf("can't highlight file: %w", err)
			return m, tea.Quit
		}
		m.fileViewport.SetContent(highlightedFile)
	case modelStateBrowsingCommitFileContents:
		m.fileViewport, cmd = m.fileViewport.Update(msg)
	case modelStateSearching:
		m.searchInput, cmd = m.searchInput.Update(msg)
	}
	return m, cmd
}

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("error: %v", m.err)
	}
	var view string
	switch m.state {
	case modelStateLoadingModules:
		view = m.spinner.View() + " Loading modules"
	case modelStateLoadingCommits:
		view = m.spinner.View() + " Loading commits"
	case modelStateLoadingCommitFileContents:
		view = m.spinner.View() + " Loading commit file contents"
	case modelStateLoadingReference:
		view = m.spinner.View() + " Loading reference"
	case modelStateBrowsingModules:
		if len(m.currentModules) == 0 {
			view += fmt.Sprintf("No modules found for owner; use %s to search for another owner", keys.Search.Keys())
		} else {
			view += m.moduleList.View()
		}
	case modelStateBrowsingCommits:
		if len(m.currentCommits) == 0 {
			view += "No commits found for module"
		} else {
			view += m.commitList.View()
		}
	case modelStateBrowsingCommitContents, modelStateBrowsingCommitFileContents:
		fileView := m.fileViewport.View()
		if m.state == modelStateBrowsingCommitFileContents {
			fileViewStyle := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder(), true).
				BorderForeground(bufBlue)
			fileView = fileViewStyle.Render(fileView)
		} else {
			fileViewStyle := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder(), true).
				BorderForeground(bufTeal)
			fileView = fileViewStyle.Render(fileView)
		}
		view = lipgloss.JoinHorizontal(
			lipgloss.Top,
			m.commitFilesList.View(),
			fileView,
		)
	case modelStateSearching:
		header := "Search for an owner (user or organization)"
		view = header + "\n\n" + m.searchInput.View()
		view += "\n\n" + m.help.View(m)
	default:
		return fmt.Sprintf("unaccounted state: %v", m.state)
	}
	return view
}

type errMsg struct{ err error }

// For messages that contain errors it's often handy to also implement the
// error interface on the message.
func (e errMsg) Error() string { return e.err.Error() }

// getUserTokenFromNetrc returns the username and token for the remote in the
// ~/.netrc file, if it exists.
func getUserTokenFromNetrc(remote string) (username string, token string, err error) {
	currentUser, err := user.Current()
	if err != nil {
		return "", "", fmt.Errorf("getting current user: %s", err)
	}
	netrcPath := filepath.Join(currentUser.HomeDir, ".netrc")
	// Give up if we can't stat the netrcPath.
	if _, err := os.Stat(netrcPath); err != nil {
		return "", "", nil
	}
	parsedNetrc, err := netrc.Parse(netrcPath)
	if err != nil {
		return "", "", fmt.Errorf("parsing netrc: %s", err)
	}
	username = parsedNetrc.Machine(remote).Get("login")
	token = parsedNetrc.Machine(remote).Get("password")
	return username, token, nil
}

func highlightFile(filename, fileContents string) (string, error) {
	// There are only a few filetypes that can actually exist in a module:
	// - LICENSE
	// - Documentation files (markdown)
	// - protobuf
	// Ref: https://buf.build/bufbuild/registry/docs/main:buf.registry.module.v1#buf.registry.module.v1.FileType
	// Fallback is for LICENSE files.
	lexer := cmp.Or(lexers.Match(filename), lexers.Fallback)
	// TODO: Make this configurable?
	// Probably not ;)
	style := cmp.Or(styles.Get("algol_nu"), styles.Fallback)
	// TODO: This seemingly works on my terminal, but we may need
	// to select a different one based on terminal type.
	// I think we should be able to figure that out from
	// tea/termenv, somehow.
	formatter := cmp.Or(formatters.TTY256, formatters.Fallback)
	iterator, err := lexer.Tokenise(nil, fileContents)
	if err != nil {
		return "", fmt.Errorf("tokenizing file: %w", err)
	}
	var buffer bytes.Buffer
	if err := formatter.Format(&buffer, style, iterator); err != nil {
		return "", fmt.Errorf("formatting file: %w", err)
	}
	return buffer.String(), nil
}

func parseReference(reference string) (remote string, resourceRef *modulev1.ResourceRef_Name, err error) {
	if reference == "" {
		// Empty reference is fine.
		return "", nil, nil
	}
	slashCount := strings.Count(reference, "/")
	if slashCount != 1 && slashCount != 2 {
		return "", nil, fmt.Errorf("expecting reference of form {<remote>/}<owner>/<module>{:<ref>}, got %s", reference)
	}
	if strings.Count(reference, ":") > 1 {
		return "", nil, fmt.Errorf(`expecting reference of form {<remote>/}<owner>/<module>{:<ref>}, got multiple ":" in %s`, reference)
	}
	first, reference, hasReference := strings.Cut(reference, ":")
	if slashCount == 2 {
		var rest string
		var valid bool
		remote, rest, valid = strings.Cut(first, "/")
		if !valid {
			panic(fmt.Errorf("strings.Cut should be valid after check"))
		}
		first = rest
	}
	owner, module, valid := strings.Cut(first, "/")
	// There must be a "/", regardless of anything else.
	if !valid {
		// We know this is true, from the check above.
		panic(fmt.Errorf("strings.Cut should be valid after check"))
	}
	moduleRef := &modulev1.ResourceRef_Name{
		Owner:  owner,
		Module: module,
	}
	// Not sure this needs to be separate, but we'll do it anyway.
	if hasReference {
		moduleRef.Child = &modulev1.ResourceRef_Name_Ref{
			Ref: reference,
		}
	}
	validator, err := protovalidate.New()
	if err != nil {
		return "", nil, fmt.Errorf("creating new protovalidator: %w", err)
	}
	if err := validator.Validate(moduleRef); err != nil {
		return "", nil, fmt.Errorf("validating reference: %w", err)
	}
	// TODO: Validate remote
	// TODO: Can we use protovalidate/cel-go for this?
	return remote, moduleRef, nil
}

// keyMap defines a set of keybindings. To work for help it must satisfy
// key.Map. It could also very easily be a map[string]key.Binding.
type keyMap struct {
	Up     key.Binding
	Down   key.Binding
	Left   key.Binding
	Right  key.Binding
	Search key.Binding
	Enter  key.Binding
	Help   key.Binding
	Quit   key.Binding
}

var keys = keyMap{
	// TODO: Ideally we'd pull these from the viewing model KeyMap defaults
	// (e.g. m.fileViewport.KeyMap); for now just give some basics.
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "move up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "move down"),
	),
	Left: key.NewBinding(
		key.WithKeys("left", "h"),
		key.WithHelp("←/h", "go out"),
	),
	Right: key.NewBinding(
		key.WithKeys("right", "l"),
		key.WithHelp("→/l", "go in"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "search"),
	),
	Search: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "search for owner"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "toggle help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "esc", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}

func (m model) ShortHelp() []key.Binding {
	var shortHelp []key.Binding
	switch m.state {
	case modelStateBrowsingModules:
		// Can't go Left while browsing modules; already at the "top".
		shortHelp = []key.Binding{keys.Up, keys.Down}
		if len(m.currentModules) == 0 {
			// Can't go Right when no modules exist.
			shortHelp = append(shortHelp, keys.Right)
		}
	case modelStateBrowsingCommits, modelStateBrowsingCommitContents:
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Left}
		if len(m.currentCommits) == 0 {
			// Can't go Right when no commits exist.
			shortHelp = append(shortHelp, keys.Right)
		}
	case modelStateBrowsingCommitFileContents:
		// Can't go Right while browsing file contents; already at the "bottom".
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Left}
	case modelStateSearching:
		shortHelp = []key.Binding{keys.Enter}
	default:
		// In the other states, just show Help and Quit.
		return []key.Binding{keys.Help, keys.Quit}
	}
	// Always show the Help key.
	return append(shortHelp, keys.Help)
}

func (m model) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		m.ShortHelp(),
		{keys.Search, keys.Help, keys.Quit},
	}
}

func newSearchInput() textinput.Model {
	searchInput := textinput.New()
	// Style the input.
	// TODO: This is probably too much.
	searchStyle := lipgloss.NewStyle().Foreground(bufBlue).Background(bufTeal)
	searchInput.PromptStyle = searchStyle
	searchInput.PlaceholderStyle = searchStyle
	searchInput.TextStyle = searchStyle
	searchInput.Cursor.Style = searchStyle
	searchInput.Focus()
	searchInput.Placeholder = "bufbuild"
	searchInput.Width = 20
	return searchInput
}

type module struct {
	underlying *modulev1.Module
}

// FilterValue implements list.Item.
func (m *module) FilterValue() string {
	return m.underlying.Name
}

// Title implements list.DefaultItem.
func (m *module) Title() string {
	// TODO: Incorporate visibility / state here?
	return m.underlying.Name
}

// Description implements list.DefaultItem.
func (m *module) Description() string {
	// TODO: Show module description.
	return m.underlying.Description
}

type commit struct {
	underlying *modulev1.Commit
}

// FilterValue implements list.Item.
func (m *commit) FilterValue() string {
	// TODO: What to filter on?
	return m.underlying.Id
}

// Title implements list.DefaultItem.
func (m *commit) Title() string {
	return m.underlying.Id
}

// Description implements list.DefaultItem.
func (m *commit) Description() string {
	// TODO: Support absolute/relative time.
	return fmt.Sprintf("Create Time: %s", m.underlying.CreateTime.AsTime().Format(time.RFC3339Nano))
}

type commitFile struct {
	underlying *modulev1.File
}

// FilterValue implements list.Item.
func (m *commitFile) FilterValue() string {
	return m.underlying.Path
}

// Title implements list.DefaultItem.
func (m *commitFile) Title() string {
	return m.underlying.Path
}

// Description implements list.DefaultItem.
// The description for a commit file is not shown.
func (m *commitFile) Description() string {
	return ""
}
