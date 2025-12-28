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
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	"buf.build/go/protovalidate"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/bufbuild/httplb"
	"github.com/charmbracelet/bubbles/v2/help"
	"github.com/charmbracelet/bubbles/v2/key"
	"github.com/charmbracelet/bubbles/v2/list"
	"github.com/charmbracelet/bubbles/v2/spinner"
	"github.com/charmbracelet/bubbles/v2/textinput"
	"github.com/charmbracelet/bubbles/v2/viewport"
	tea "github.com/charmbracelet/bubbletea/v2"
	"github.com/charmbracelet/lipgloss/v2"
	"github.com/charmbracelet/lipgloss/v2/compat"
	"github.com/cli/browser"
	"github.com/jdx/go-netrc"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

const (
	bufBlue = "#0e5df5"
	bufTeal = "#5fdcff"

	defaultRemote = "buf.build"
)

var (
	colorForeground = compat.AdaptiveColor{
		Light: lipgloss.Color(bufBlue),
		Dark:  lipgloss.Color(bufTeal),
	}
	colorBackground = compat.AdaptiveColor{
		Light: lipgloss.Color(bufTeal),
		Dark:  lipgloss.Color(bufBlue),
	}
	codeStyleLight = styles.Get("modus-operandi")
	codeStyleDark  = styles.Get("modus-vivendi")
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}

func run(_ context.Context, args []string) error {
	fs := ff.NewFlagSet("buftui")
	var (
		// `-r` is for reference, which should generally be preferred.
		remoteFlag    = fs.StringLong("remote", "", "BSR remote")
		tokenFlag     = fs.String('t', "token", "", "Set token for authentication (default: password for remote in ~/.netrc)")
		referenceFlag = fs.String('r', "reference", "", "Set BSR reference to open")
	)
	if err := ff.Parse(fs, args); err != nil {
		fmt.Printf("%s\n", ffhelp.Flags(fs))
		return err
	}

	parsedRemote, parsedReference, err := parseReference(*referenceFlag)
	if err != nil {
		return fmt.Errorf("parsing reference flag: %w", err)
	}
	if parsedRemote != "" && *remoteFlag != "" && *remoteFlag != parsedRemote {
		return fmt.Errorf("cannot provide conflicting `--remote` flag (%s) and reference remote (%s)", *remoteFlag, parsedRemote)
	}
	// We know the remotes at least aren't conflicting, so take whichever is non-empty.
	remote := cmp.Or(parsedRemote, *remoteFlag, defaultRemote)
	// Sanity check for `--remote ""`, or an invalid parsed reference.
	if remote == "" {
		return fmt.Errorf("remote cannot be empty")
	}

	token := *tokenFlag
	if token == "" {
		var err error
		token, err = getTokenFromNetrc(remote)
		if err != nil {
			return fmt.Errorf("getting netrc credentials for remote %q: %w", remote, err)
		}
	}

	httpClient := httplb.NewClient()
	defer httpClient.Close()

	initialState := modelStateNavigating
	if parsedReference != nil {
		initialState = modelStateLoadingReference
	}

	delegate := list.NewDefaultDelegate()
	moduleList := list.New(nil, delegate, 20, 20)
	moduleList.SetShowHelp(false)

	commitList := list.New(nil, delegate, 20, 20)
	commitList.SetShowHelp(false)

	commitFilesList := list.New(nil, delegate, 20, 20)
	commitFilesList.SetShowHelp(false)

	model := model{
		state:            initialState,
		spinner:          spinner.New(spinner.WithSpinner(spinner.Dot)),
		client:           newClient(httpClient, remote, token),
		help:             help.New(),
		keys:             keys,
		currentReference: parsedReference,
		navigateInput:    newNavigateInput(false),
		remote:           remote,
		fileViewport:     viewport.New(),

		moduleList:      moduleList,
		commitList:      commitList,
		commitFilesList: commitFilesList,
	}

	if _, err := tea.NewProgram(model, tea.WithAltScreen()).Run(); err != nil {
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
	modelStateNavigating
	modelStateLoadingReference
	modelStateLoadingModules
	modelStateLoadingCommits
	modelStateLoadingCommitFileContents
)

type model struct {
	// (Basically) Static
	listStyles     list.Styles
	listItemStyles list.DefaultItemStyles
	spinner        spinner.Model
	client         *client
	keys           keyMap
	remote         string

	// State - where are we?
	state modelState
	// Should exit when setting this.
	err error

	// State-related data
	currentOwner       string
	currentModule      string
	currentCommitID    string
	currentModules     modulesMsg
	currentCommits     commitsMsg
	currentCommitFiles []*modulev1.File
	currentReference   *modulev1.ResourceRef_Name

	// Sub-models
	moduleList      list.Model
	commitList      list.Model
	commitFilesList list.Model
	fileViewport    viewport.Model
	navigateInput   textinput.Model
	help            help.Model

	isDark bool
}

func (m model) Init() tea.Cmd {
	inits := []tea.Cmd{
		m.spinner.Tick,
		tea.RequestBackgroundColor,
	}
	if m.currentReference != nil {
		inits = append(inits, m.client.getResource(m.currentReference))
	}
	return tea.Batch(inits...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.isDark = msg.IsDark()

		listStyles := list.DefaultStyles(msg.IsDark())
		listStyles.Title = listStyles.Title.Foreground(colorForeground).Background(colorBackground).Bold(true)
		m.listStyles = listStyles

		listItemStyles := list.NewDefaultItemStyles(msg.IsDark())
		listItemStyles.SelectedTitle = listItemStyles.SelectedTitle.Foreground(colorForeground).BorderLeftForeground(colorForeground).Bold(true)
		listItemStyles.SelectedDesc = listItemStyles.SelectedDesc.Foreground(colorForeground).BorderLeftForeground(colorForeground)
		listItemStyles.NormalTitle = listItemStyles.NormalTitle.Foreground(colorForeground)
		m.listItemStyles = listItemStyles

		{
			delegate := list.NewDefaultDelegate()
			delegate.Styles = listItemStyles
			// TODO: Show module description.
			delegate.ShowDescription = false
			delegate.SetSpacing(0)
			m.moduleList.SetDelegate(delegate)
		}

		{
			delegate := list.NewDefaultDelegate()
			delegate.Styles = listItemStyles
			m.commitList.SetDelegate(delegate)
		}

		{
			delegate := list.NewDefaultDelegate()
			delegate.Styles = listItemStyles
			delegate.ShowDescription = false
			delegate.SetSpacing(0)
			m.commitFilesList.SetDelegate(delegate)
		}

	case tea.WindowSizeMsg:
		// If we set a width on the help menu it can gracefully truncate
		// its view as needed.
		m.help.Width = msg.Width

		// TODO: Make these values responsive, based on the number of items received; these
		// should be the max values.
		m.moduleList.SetHeight(msg.Height - 5) // Give space for the list title and help message
		m.moduleList.SetWidth(msg.Width)
		m.commitList.SetHeight(msg.Height - 5) // Give space for the list title and help message
		m.commitList.SetWidth(msg.Width)
		m.commitFilesList.SetHeight(msg.Height - 5) // Give space for the list title and help message
		m.commitFilesList.SetWidth(msg.Width / 2)
		m.fileViewport.SetHeight(msg.Height)
		m.fileViewport.SetWidth(msg.Width / 2)
		m.navigateInput.SetWidth(min(msg.Width, 50)) // clamped at 50 characters wide

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
			m.currentCommitID = retrievedResource.Commit.Id
			m.state = modelStateLoadingCommitFileContents
			return m, m.client.getCommitContent(m.currentCommitID)
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
		m.moduleList.SetItems(modules)
		m.moduleList.Title = fmt.Sprintf("Modules (Owner: %s)", m.currentOwner)
		m.moduleList.Styles = m.listStyles
		m.moduleList.InfiniteScrolling = false
		m.moduleList.AdditionalFullHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Right}
		}
		m.moduleList.AdditionalShortHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Right}
		}
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
		m.commitList.SetItems(commits)
		m.commitList.Title = fmt.Sprintf("Commits (Module: %s/%s)", m.currentOwner, m.currentModule)
		m.commitList.Styles = m.listStyles
		m.commitList.InfiniteScrolling = false
		m.commitList.AdditionalFullHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Left, keys.Right}
		}
		m.commitList.AdditionalShortHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Left, keys.Right}
		}
		return m, nil

	case contentsMsg:
		m.state = modelStateBrowsingCommitContents
		m.currentCommitFiles = msg.Files
		commitFiles := make([]list.Item, len(m.currentCommitFiles))
		for i, currentCommitFile := range m.currentCommitFiles {
			commitFiles[i] = &commitFile{currentCommitFile}
		}
		m.commitFilesList.SetItems(commitFiles)
		m.commitFilesList.Title = fmt.Sprintf("Commit %s (Module: %s/%s)", m.currentCommitID, m.currentOwner, m.currentModule)
		m.commitFilesList.Styles = m.listStyles
		m.commitFilesList.InfiniteScrolling = false
		m.commitFilesList.AdditionalFullHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Left, keys.Right}
		}
		m.commitFilesList.AdditionalShortHelpKeys = func() []key.Binding {
			return []key.Binding{keys.Left, keys.Right}
		}
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
		highlightedFile, err := highlightFile(selectedFileName, fileContents, m.isDark)
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
		case key.Matches(msg, m.keys.Navigate):
			// From anywhere other than the navigate state, "g"
			// enters a navigate state.
			if m.state != modelStateNavigating {
				// "g" -> "navigate"
				m.state = modelStateNavigating
				m.navigateInput.Reset()
				return m, nil
			}
		case key.Matches(msg, m.keys.Enter):
			switch m.state {
			case modelStateNavigating:
				if m.navigateInput.Err != nil {
					// Don't allow a submission if the input doesn't validate.
					return m, nil
				}
				navigateValue := m.navigateInput.Value()
				// Try to parse as a reference
				parsedRemote, parsedReference, err := parseReference(navigateValue)
				if err == nil && parsedReference != nil {
					// It's a reference, navigate directly to it
					if parsedRemote != "" && m.remote != parsedRemote && parsedRemote != defaultRemote {
						m.err = fmt.Errorf("cannot navigate to reference on different remote (%s) than current remote (%s)", parsedRemote, m.remote)
						return m, nil
					}
					m.currentReference = parsedReference
					m.state = modelStateLoadingReference
					return m, m.client.getResource(parsedReference)
				}
				// Otherwise, treat it as an owner
				m.currentOwner = navigateValue
				// TODO: Clear navigate input?
				return m, m.client.listModules(m.currentOwner)
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
				m.currentCommitID = commit.underlying.Id
				return m, m.client.getCommitContent(m.currentCommitID)
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
				m.commitFilesList.ResetSelected()
				return m, m.client.listCommits(m.currentOwner, m.currentModule)
			case modelStateBrowsingCommits:
				// NOTE: We don't necessarily have the module
				// list populated, because we may have gone
				// directly to a reference.
				// TODO: Hook this up to caching.
				m.state = modelStateLoadingModules
				m.commitList.ResetSelected()
				return m, m.client.listModules(m.currentOwner)
			}
		case key.Matches(msg, m.keys.Browse):
			var url string
			var list list.Model
			switch m.state {
			case modelStateBrowsingCommitFileContents:
				list = m.commitFilesList
				// TODO: Can we go directly to the line that the user is viewing?
				// For now, do the same as modelStateBrowsingCommitContents.
				commitFile, ok := m.commitFilesList.SelectedItem().(*commitFile)
				if !ok {
					panic("selected item is not a commitFile")
				}
				url = "https://" + m.remote + "/" + m.currentOwner + "/" + m.currentModule + "/file/" + m.currentCommitID + ":" + commitFile.underlying.Path
			case modelStateBrowsingCommitContents:
				list = m.commitFilesList
				commitFile, ok := m.commitFilesList.SelectedItem().(*commitFile)
				if !ok {
					panic("selected item is not a commitFile")
				}
				url = "https://" + m.remote + "/" + m.currentOwner + "/" + m.currentModule + "/file/" + m.currentCommitID + ":" + commitFile.underlying.Path
			case modelStateBrowsingCommits:
				list = m.commitList
				commit, ok := m.commitList.SelectedItem().(*commit)
				if !ok {
					panic("selected item is not a commit")
				}
				url = "https://" + m.remote + "/" + m.currentOwner + "/" + m.currentModule + "/tree/" + commit.underlying.Id
			case modelStateBrowsingModules:
				list = m.moduleList
				module, ok := m.moduleList.SelectedItem().(*module)
				if !ok {
					panic("selected item is not a module")
				}
				url = "https://" + m.remote + "/" + m.currentOwner + "/" + module.underlying.Name
			}

			if url != "" {
				if err := browser.OpenURL(url); err != nil {
					m.err = fmt.Errorf("opening URL %q: %w", url, err)
					return m, nil
				}
				// TODO: Where does this show? Does it need more time?
				return m, list.NewStatusMessage("opened " + url)
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
		highlightedFile, err := highlightFile(selectedFileName, fileContents, m.isDark)
		if err != nil {
			m.err = fmt.Errorf("can't highlight file: %w", err)
			return m, tea.Quit
		}
		m.fileViewport.SetContent(highlightedFile)
		// When we switch files, we reset the position of the viewport back to the top.
		m.fileViewport.GotoTop()
	case modelStateBrowsingCommitFileContents:
		m.fileViewport, cmd = m.fileViewport.Update(msg)
	case modelStateNavigating:
		m.navigateInput, cmd = m.navigateInput.Update(msg)
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
			view += fmt.Sprintf("No modules found for owner; use %s to navigate to another owner", keys.Navigate.Keys())
		} else {
			view += m.moduleList.View()
		}
		view += "\n\n" + m.help.View(m)
	case modelStateBrowsingCommits:
		if len(m.currentCommits) == 0 {
			view += "No commits found for module"
		} else {
			view += m.commitList.View()
		}
		view += "\n\n" + m.help.View(m)
	case modelStateBrowsingCommitContents, modelStateBrowsingCommitFileContents:
		fileViewStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true)
		if m.state == modelStateBrowsingCommitFileContents {
			fileViewStyle = fileViewStyle.BorderForeground(colorForeground)
		} else {
			fileViewStyle = fileViewStyle.BorderForeground(colorBackground)
		}
		view = lipgloss.JoinHorizontal(
			lipgloss.Top,
			m.commitFilesList.View(),
			fileViewStyle.Render(m.fileViewport.View()),
		)
		view += "\n\n" + m.help.View(m)
	case modelStateNavigating:
		header := "Navigate to owner or reference (e.g., owner/module or owner/module:ref)"
		view = header + "\n\n" + m.navigateInput.View()
		if m.navigateInput.Err != nil {
			view += "\n\n" + fmt.Sprintf("err: %s", m.navigateInput.Err)
		}
	default:
		return fmt.Sprintf("unaccounted state: %v", m.state)
	}
	return view
}

type errMsg struct{ err error }

// For messages that contain errors it's often handy to also implement the
// error interface on the message.
func (e errMsg) Error() string { return e.err.Error() }

// getTokenFromNetrc returns the token for the remote in the ~/.netrc file, if it exists.
func getTokenFromNetrc(remote string) (string, error) {
	currentUser, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("getting current user: %s", err)
	}
	netrcPath := filepath.Join(currentUser.HomeDir, ".netrc")
	// Give up if we can't stat the netrcPath.
	if _, err := os.Stat(netrcPath); err != nil {
		return "", nil
	}
	parsedNetrc, err := netrc.Parse(netrcPath)
	if err != nil {
		return "", fmt.Errorf("parsing netrc: %s", err)
	}
	netrcRemote := parsedNetrc.Machine(remote)
	if netrcRemote == nil {
		// We don't have the remote in the .netrc; abort.
		return "", nil
	}
	token := netrcRemote.Get("password")
	return token, nil
}

func highlightFile(filename, fileContents string, isDark bool) (string, error) {
	// There are only a few filetypes that can actually exist in a module:
	// - LICENSE
	// - Documentation files (markdown)
	// - protobuf
	// Ref: https://buf.build/bufbuild/registry/docs/main:buf.registry.module.v1#buf.registry.module.v1.FileType
	// Fallback is for LICENSE files.
	lexer := cmp.Or(lexers.Match(filename), lexers.Fallback)
	// TODO: Make this configurable?
	style := codeStyleLight
	if isDark {
		style = codeStyleDark
	}
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
	if err := protovalidate.Validate(moduleRef); err != nil {
		return "", nil, fmt.Errorf("validating reference: %w", err)
	}
	// TODO: Validate remote
	// TODO: Can we use protovalidate/cel-go for this?
	return remote, moduleRef, nil
}

// keyMap defines a set of keybindings. To work for help it must satisfy
// key.Map. It could also very easily be a map[string]key.Binding.
type keyMap struct {
	Up   key.Binding
	Down key.Binding
	Left key.Binding
	Right key.Binding
	Navigate key.Binding
	Enter key.Binding
	Help key.Binding
	Quit key.Binding
	Browse key.Binding
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
		key.WithHelp("enter", "navigate"),
	),
	Navigate: key.NewBinding(
		key.WithKeys("g"),
		key.WithHelp("g", "navigate to owner/module"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "toggle help"),
	),
	Quit: key.NewBinding(
		key.WithKeys("esc", "ctrl+c"),
		key.WithHelp("esc", "quit"),
	),
	Browse: key.NewBinding(
		key.WithKeys("o"),
		key.WithHelp("o", "open in browser"),
	),
}

func (m model) ShortHelp() []key.Binding {
	var shortHelp []key.Binding
	switch m.state {
	case modelStateBrowsingModules:
		// Can't go Left while browsing modules; already at the "top".
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Browse}
		if len(m.currentModules) != 0 {
			// Can only go right when modules exist.
			shortHelp = append(shortHelp, keys.Right)
		}
	case modelStateBrowsingCommits, modelStateBrowsingCommitContents:
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Left}
		if len(m.currentCommits) != 0 {
			// Can only go right when commits exist.
			shortHelp = append(shortHelp, keys.Right)
		}
	case modelStateBrowsingCommitFileContents:
		// Can't go Right while browsing file contents; already at the "bottom".
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Left}
	case modelStateNavigating:
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
		{keys.Navigate, keys.Help, keys.Quit},
	}
}

func newNavigateInput(isDark bool) textinput.Model {
	input := textinput.New()
	input.Validate = func(inputStr string) error {
		// Try to parse as a reference first
		if _, _, err := parseReference(inputStr); err == nil {
			return nil
		}
		// Fall back to validating as an owner name
		return protovalidate.Validate(&ownerv1.OwnerRef{
			Value: &ownerv1.OwnerRef_Name{Name: inputStr},
		})
	}
	// Style the input.
	input.Styles = textinput.DefaultStyles(isDark)
	style := lipgloss.NewStyle().Foreground(colorForeground).Background(colorBackground)
	input.Styles.Focused.Placeholder = style
	input.Styles.Focused.Prompt = style
	input.Styles.Focused.Text = style
	input.Styles.Cursor.Color = colorBackground

	input.Focus()
	input.Placeholder = "bufbuild/registry:main"
	return input
}

type module struct {
	underlying *modulev1.Module
}

// FilterValue implements [list.Item].
func (m *module) FilterValue() string {
	return m.underlying.Name
}

// Title implements [list.DefaultItem].
func (m *module) Title() string {
	var title string
	if m.underlying.Visibility == modulev1.ModuleVisibility_MODULE_VISIBILITY_PRIVATE {
		title += "􀎠"
	}
	title += m.underlying.Name
	if m.underlying.State == modulev1.ModuleState_MODULE_STATE_DEPRECATED {
		title += " (Deprecated)"
	}
	return title
}

// Description implements [list.DefaultItem].
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
	return fmt.Sprintf("Create Time: %s", m.underlying.CreateTime.AsTime().Format(time.Stamp))
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
