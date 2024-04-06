package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"buf.build/gen/go/bufbuild/registry/connectrpc/go/buf/registry/module/v1/modulev1connect"
	modulev1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1"
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	"connectrpc.com/connect"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/bufbuild/httplb"
	"github.com/bufbuild/protovalidate-go"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jdx/go-netrc"
	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"
)

type modelState string

const (
	modelStateBrowsingModules            modelState = "browsing-modules"
	modelStateBrowsingCommits            modelState = "browsing-commits"
	modelStateBrowsingCommitContents     modelState = "browsing-commit-contents"
	modelStateBrowsingCommitFileContents modelState = "browsing-commit-file-contents"
	modelStateSearching                  modelState = "searching"
	modelStateLoading                    modelState = "loading"
)

type model struct {
	state modelState

	spinner spinner.Model

	// TODO: Share a single table and just hold on to the messages and
	// re-render?
	moduleTable        table.Model
	noOwnerModules     bool
	commitsTable       table.Model
	noModuleCommits    bool
	commitFilesTable   table.Model
	currentModule      string
	currentCommit      string
	currentCommitFiles []*modulev1.File
	fileViewport       viewport.Model
	searchInput        textinput.Model
	help               help.Model

	keys keyMap

	registryDomain string
	username       string
	token          string
	moduleOwner    string

	err error

	tableStyles table.Styles

	httpClient connect.HTTPClient

	currentReference *modulev1.ResourceRef_Name
}

const (
	bufBlue = lipgloss.Color("#151fd5")
	bufTeal = lipgloss.Color("#91dffb")

	tuuidWidth = 32
)

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

func (m model) ShortHelp() []key.Binding {
	var shortHelp []key.Binding
	switch m.state {
	case modelStateBrowsingModules:
		// Can't go Left while browsing modules; already at the "top".
		if m.noOwnerModules {
			// Can't go Right when no modules exist.
			shortHelp = []key.Binding{keys.Up, keys.Down}
		} else {
			shortHelp = []key.Binding{keys.Up, keys.Down, keys.Right}
		}
	case modelStateBrowsingCommits, modelStateBrowsingCommitContents:
		if m.noModuleCommits {
			// Can't go Right when no commits exist.
			shortHelp = []key.Binding{keys.Up, keys.Down, keys.Left}
		} else {
			shortHelp = []key.Binding{keys.Up, keys.Down, keys.Right, keys.Left}
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

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}

func run(_ context.Context) error {
	fs := ff.NewFlagSet("buftui")
	var (
		registrydomainFlag = fs.String('d', "domain", "buf.build", "BSR registry domain")
		fullscreenFlag     = fs.Bool('f', "fullscreen", "Enable fullscreen display")
		usernameFlag       = fs.String('u', "username", "", "Set username for authentication (default: login for registry hostname in ~/.netrc)")
		tokenFlag          = fs.String('t', "token", "", "Set token for authentication (default: password for registry hostname in ~/.netrc)")
		referenceFlag      = fs.String('r', "reference", "", "Set BSR reference to open")
	)
	if err := ff.Parse(fs, os.Args[1:]); err != nil {
		fmt.Printf("%s\n", ffhelp.Flags(fs))
		return err
	}

	username := *usernameFlag
	token := *tokenFlag

	// Either username && token should be provided at the CLI, or they should be loaded from the ~/.netrc.
	if (username == "" && token != "") || (username != "" && token == "") {
		return fmt.Errorf("must set both username and token flags, or neither (and load authentication from ~/.netrc)")
	}
	if username == "" && token == "" {
		var err error
		username, token, err = getUserTokenFromNetrc(*registrydomainFlag)
		if err != nil {
			return fmt.Errorf("getting netrc credentials: %w", err)
		}
	}
	if username == "" {
		return fmt.Errorf("username must be set, either by flag or in the ~/.netrc file")
	}
	if token == "" {
		return fmt.Errorf("token must be set, either by flag or in the ~/.netrc file")
	}

	initialReference, err := parseReference(*referenceFlag)
	if err != nil {
		return fmt.Errorf("parsing reference flag: %w", err)
	}

	tableStyles := table.DefaultStyles()
	tableStyles.Header = tableStyles.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(bufBlue).
		BorderBottom(true).
		Bold(false)
	tableStyles.Selected = tableStyles.Selected.
		Foreground(bufTeal).
		Background(bufBlue).
		Bold(false)

	httpClient := httplb.NewClient()
	defer httpClient.Close()

	model := model{
		state:            modelStateLoading,
		registryDomain:   *registrydomainFlag,
		username:         username,
		token:            token,
		moduleOwner:      username,
		spinner:          spinner.New(),
		tableStyles:      tableStyles,
		httpClient:       httpClient,
		help:             help.New(),
		keys:             keys,
		currentReference: initialReference,
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

func (m model) Init() tea.Cmd {
	if m.currentReference != nil {
		return m.getResource(m.currentReference)
	}
	return m.getModules()
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
			m.moduleOwner = msg.requestedResource.Owner
			m.currentModule = retrievedResource.Module.Name
			m.state = modelStateLoading
			return m, m.listCommits()
		case *modulev1.Resource_Commit:
			m.moduleOwner = msg.requestedResource.Owner
			m.currentModule = msg.requestedResource.Module
			m.currentCommit = retrievedResource.Commit.Id
			return m, m.getCommitContent(m.currentCommit)
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
		if len(msg) == 0 {
			m.noOwnerModules = true
			return m, nil
		}
		// Reset.
		m.noOwnerModules = false
		columns := []table.Column{
			// TODO: adjust these dynamically?
			// NOTE: It seems like module.{Description,Url} are not
			// currently widely populated; leaving those out
			// deliberately.
			{Title: "ID", Width: tuuidWidth},
			{Title: "Name", Width: 20},
			{Title: "Create Time", Width: 19},
			{Title: "Visibility", Width: 10},
			{Title: "State", Width: 10},
		}
		tableHeight := len(msg)
		rows := make([]table.Row, len(msg))
		for i, module := range msg {
			var visibility string
			switch module.Visibility {
			case modulev1.ModuleVisibility_MODULE_VISIBILITY_PRIVATE:
				visibility = "private"
			case modulev1.ModuleVisibility_MODULE_VISIBILITY_PUBLIC:
				visibility = "public"
			default:
				visibility = "unknown"
			}
			var state string
			switch module.State {
			case modulev1.ModuleState_MODULE_STATE_ACTIVE:
				state = "active"
			case modulev1.ModuleState_MODULE_STATE_DEPRECATED:
				state = "deprecated"
			default:
				state = "unknown"
			}
			rows[i] = table.Row{
				module.Id,
				module.Name,
				module.CreateTime.AsTime().Format(time.DateTime),
				visibility,
				state,
			}
		}
		m.moduleTable = table.New(
			table.WithColumns(columns),
			table.WithRows(rows),
			table.WithFocused(true),
			table.WithHeight(tableHeight),
			table.WithStyles(m.tableStyles),
		)
		return m, nil

	case commitsMsg:
		m.state = modelStateBrowsingCommits
		if len(msg) == 0 {
			m.noModuleCommits = true
			return m, nil
		}
		// Reset.
		m.noModuleCommits = false

		columns := []table.Column{
			// TODO: adjust these dynamically?
			{Title: "ID", Width: tuuidWidth},
			{Title: "Create Time", Width: 19},
			// No need to make this too long - it's not really
			// useful to consumers.
			{Title: "b5 Digest", Width: 9},
			// TODO: What else is useful here?
		}
		rows := make([]table.Row, len(msg))
		for i, commit := range msg {
			rows[i] = table.Row{
				commit.Id,
				commit.CreateTime.AsTime().Format(time.DateTime),
				fmt.Sprintf("%x", commit.Digest.Value),
			}
		}
		m.commitsTable = table.New(
			table.WithColumns(columns),
			table.WithRows(rows),
			table.WithFocused(true),
			table.WithHeight(len(msg)),
			table.WithStyles(m.tableStyles),
		)
		return m, nil

	case contentsMsg:
		// TODO: This is a stupid way to display files - eventually, improve it.
		columns := []table.Column{
			{Title: "Path", Width: 50},
		}
		rows := make([]table.Row, len(msg.Files))
		// TODO: Cap this at something sane based on the terminal size?
		tableHeight := len(msg.Files)
		// This is probably not even possible?
		if len(msg.Files) == 0 {
			rows = append(rows, table.Row{
				"No files found for commit",
			})
			tableHeight = 1
		} else {
			for i, file := range msg.Files {
				rows[i] = table.Row{file.Path}
			}
		}
		m.commitFilesTable = table.New(
			table.WithColumns(columns),
			table.WithRows(rows),
			table.WithFocused(true),
			table.WithHeight(tableHeight),
			table.WithStyles(m.tableStyles),
		)
		m.state = modelStateBrowsingCommitContents
		m.currentCommitFiles = msg.Files
		m.fileViewport = viewport.New(100, max(tableHeight, 30))
		for _, file := range m.currentCommitFiles {
			if file.Path == m.commitFilesTable.SelectedRow()[0] {
				m.fileViewport.SetContent(string(file.Content))
			}
		}
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
				m.searchInput = searchInput
				return m, nil
			}
		case key.Matches(msg, m.keys.Enter):
			switch m.state {
			case modelStateSearching:
				m.moduleOwner = m.searchInput.Value()
				// TODO: Clear search input?
				return m, m.getModules()
			}
			// enter or l are equivalent for all the cases below.
			fallthrough
		case key.Matches(msg, m.keys.Right):
			switch m.state {
			case modelStateBrowsingModules:
				if m.noOwnerModules {
					// Don't do anything.
					return m, nil
				}
				m.state = modelStateLoading
				m.currentModule = m.moduleTable.SelectedRow()[1] // module name row
				return m, m.listCommits()
			case modelStateBrowsingCommits:
				if m.noModuleCommits {
					// Don't do anything.
					return m, nil
				}
				m.state = modelStateLoading
				m.currentCommit = m.commitsTable.SelectedRow()[0] // commit name row
				return m, m.getCommitContent(m.currentCommit)
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
				m.state = modelStateBrowsingCommits
				return m, nil
			case modelStateBrowsingCommits:
				m.state = modelStateBrowsingModules
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	switch m.state {
	case modelStateBrowsingModules:
		m.moduleTable, cmd = m.moduleTable.Update(msg)
	case modelStateBrowsingCommits:
		m.commitsTable, cmd = m.commitsTable.Update(msg)
	case modelStateBrowsingCommitContents:
		m.commitFilesTable, cmd = m.commitFilesTable.Update(msg)
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
	case modelStateLoading:
		view = m.spinner.View()
	case modelStateBrowsingModules:
		header := fmt.Sprintf("Modules (Owner: %s)\n", m.moduleOwner)
		view = header
		if m.noOwnerModules {
			view += fmt.Sprintf("No modules found for owner; use %s to search for another owner", keys.Search.Keys())
		} else {
			view += m.moduleTable.View()
		}
	case modelStateBrowsingCommits:
		header := fmt.Sprintf("Commits (Module: %s/%s)\n", m.moduleOwner, m.currentModule)
		view = header
		if m.noModuleCommits {
			view += "No commits found for module"
		} else {
			view += m.commitsTable.View()
		}
	case modelStateBrowsingCommitContents, modelStateBrowsingCommitFileContents:
		selectedFileName := m.commitFilesTable.SelectedRow()[0]
		var fileContents string
		for _, file := range m.currentCommitFiles {
			if file.Path == selectedFileName {
				fileContents = string(file.Content)
				break
			}
		}
		highlightedFile, err := highlightFile(selectedFileName, fileContents)
		if err != nil {
			// TODO: Log and use the unhighlighted file contents?
			return fmt.Sprintf("highlighting selected file %s: %s", selectedFileName, err)
		}
		m.fileViewport.SetContent(highlightedFile)
		fileView := m.fileViewport.View()
		if m.state == modelStateBrowsingCommitFileContents {
			fileViewStyle := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder(), true).
				BorderForeground(bufBlue)
			fileView = fileViewStyle.Render(fileView)
		}
		view = lipgloss.JoinVertical(
			lipgloss.Left,
			fmt.Sprintf("Commit %s (Module: %s/%s)\n", m.currentCommit, m.moduleOwner, m.currentModule),
			lipgloss.JoinHorizontal(
				lipgloss.Top,
				m.commitFilesTable.View(),
				fileView,
			),
		)
	case modelStateSearching:
		header := "Search for an owner (user or organization)"
		view = header + "\n\n" + m.searchInput.View()
	default:
		return fmt.Sprintf("unaccounted state: %v", m.state)
	}
	view += "\n\n" + m.help.View(m)
	return view
}

type modulesMsg []*modulev1.Module

func (m model) getModules() tea.Cmd {
	return func() tea.Msg {
		moduleServiceClient := modulev1connect.NewModuleServiceClient(
			m.httpClient,
			"https://"+m.registryDomain,
		)
		request := connect.NewRequest(&modulev1.ListModulesRequest{
			OwnerRefs: []*ownerv1.OwnerRef{
				{
					Value: &ownerv1.OwnerRef_Name{
						Name: m.moduleOwner,
					},
				},
			},
		})
		setBasicAuthHeader(request, m.username, m.token)
		response, err := moduleServiceClient.ListModules(context.Background(), request)
		if err != nil {
			return errMsg{fmt.Errorf("listing modules: %s", err)}
		}
		return modulesMsg(response.Msg.Modules)
	}
}

type commitsMsg []*modulev1.Commit

func (m model) listCommits() tea.Cmd {
	return func() tea.Msg {
		commitServiceClient := modulev1connect.NewCommitServiceClient(
			m.httpClient,
			"https://"+m.registryDomain,
		)
		request := connect.NewRequest(&modulev1.ListCommitsRequest{
			ResourceRef: &modulev1.ResourceRef{
				Value: &modulev1.ResourceRef_Name_{
					Name: &modulev1.ResourceRef_Name{
						Owner:  m.moduleOwner,
						Module: m.currentModule,
					},
				},
			},
		})
		setBasicAuthHeader(request, m.username, m.token)
		response, err := commitServiceClient.ListCommits(context.Background(), request)
		if err != nil {
			return errMsg{fmt.Errorf("getting commits: %s", err)}
		}
		return commitsMsg(response.Msg.Commits)
	}
}

type contentsMsg *modulev1.DownloadResponse_Content

func (m model) getCommitContent(commitName string) tea.Cmd {
	return func() tea.Msg {
		downloadServiceClient := modulev1connect.NewDownloadServiceClient(
			m.httpClient,
			"https://"+m.registryDomain,
		)
		request := connect.NewRequest(&modulev1.DownloadRequest{
			Values: []*modulev1.DownloadRequest_Value{
				{
					ResourceRef: &modulev1.ResourceRef{
						Value: &modulev1.ResourceRef_Name_{
							Name: &modulev1.ResourceRef_Name{
								Owner:  m.moduleOwner,
								Module: m.currentModule,
								Child: &modulev1.ResourceRef_Name_Ref{
									Ref: commitName,
								},
							},
						},
					},
				},
			},
		})
		setBasicAuthHeader(request, m.username, m.token)
		response, err := downloadServiceClient.Download(context.Background(), request)
		if err != nil {
			return errMsg{fmt.Errorf("getting commit content: %s", err)}
		}
		if len(response.Msg.Contents) != 1 {
			return errMsg{fmt.Errorf("requested 1 commit contents, got %v", len(response.Msg.Contents))}
		}
		return contentsMsg(response.Msg.Contents[0])
	}
}

type resourceMsg struct {
	// Avoid races with other commands by ensuring that we return the
	// request with the response.
	requestedResource *modulev1.ResourceRef_Name
	retrievedResource *modulev1.Resource
}

func (m model) getResource(resourceName *modulev1.ResourceRef_Name) tea.Cmd {
	return func() tea.Msg {
		resourceServiceClient := modulev1connect.NewResourceServiceClient(
			m.httpClient,
			"https://"+m.registryDomain,
		)
		request := connect.NewRequest(&modulev1.GetResourcesRequest{
			ResourceRefs: []*modulev1.ResourceRef{
				{
					Value: &modulev1.ResourceRef_Name_{
						Name: resourceName,
					},
				},
			},
		})
		setBasicAuthHeader(request, m.username, m.token)
		response, err := resourceServiceClient.GetResources(context.Background(), request)
		if err != nil {
			return errMsg{fmt.Errorf("getting commit content: %s", err)}
		}
		if len(response.Msg.Resources) != 1 {
			return errMsg{fmt.Errorf("requested 1 commit contents, got %v", len(response.Msg.Resources))}
		}
		return resourceMsg{
			requestedResource: resourceName,
			retrievedResource: response.Msg.Resources[0],
		}
	}
}

func setBasicAuthHeader(request connect.AnyRequest, username, token string) {
	request.Header().Set(
		"Authorization",
		"Basic "+base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", username, token))),
	)
}

type errMsg struct{ err error }

// For messages that contain errors it's often handy to also implement the
// error interface on the message.
func (e errMsg) Error() string { return e.err.Error() }

// getUserTokenFromNetrc returns the username and token for the hostname in the
// ~/.netrc file, if it exists.
func getUserTokenFromNetrc(hostname string) (username string, token string, err error) {
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
	username = parsedNetrc.Machine(hostname).Get("login")
	token = parsedNetrc.Machine(hostname).Get("password")
	return username, token, nil
}

func highlightFile(filename, fileContents string) (string, error) {
	// TODO: There are only a few filetypes that can actually exist in a module:
	// - Licenses
	// - README (markdown/text(?))
	// - protobuf
	lexer := lexers.Match(filename)
	if lexer == nil {
		// This happens for LICENSE files.
		lexer = lexers.Fallback
	}
	// TODO: Make this configurable?
	// Probably not ;)
	style := styles.Get("algol_nu")
	if style == nil {
		style = styles.Fallback
	}
	// TODO: This seemingly works on my terminal, but we may need
	// to select a different one based on terminal type.
	// I think we should be able to figure that out from
	// tea/termenv, somehow.
	formatter := formatters.TTY256
	if formatter == nil {
		formatter = formatters.Fallback
	}
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

func parseReference(reference string) (*modulev1.ResourceRef_Name, error) {
	if reference == "" {
		// Empty reference is fine.
		return nil, nil
	}
	if strings.Count(reference, "/") != 1 {
		return nil, fmt.Errorf("expecting reference of either <owner>/<module> or <owner>/<module>:<ref>; got %s", reference)
	}
	if strings.Count(reference, ":") > 1 {
		return nil, fmt.Errorf("expecting reference of either <owner>/<module> or <owner>/<module>:<ref>; got %s", reference)
	}
	// We'll accept references of the form "<owner-name>/<module-name>"
	// or "<owner-name>/<module-name>:<ref>".
	ownerModule, reference, hasReference := strings.Cut(reference, ":")
	owner, module, valid := strings.Cut(ownerModule, "/")
	// There must be a "/", regardless of anything else.
	if !valid {
		return nil, fmt.Errorf("expecting reference of either <owner>/<module> or <owner>/<module>:<ref>; got %s", reference)
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
		return nil, fmt.Errorf("creating new protovalidator: %w", err)
	}
	if err := validator.Validate(moduleRef); err != nil {
		return nil, fmt.Errorf("validating reference: %w", err)
	}
	return moduleRef, nil
}
