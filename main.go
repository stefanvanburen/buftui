package main

import (
	"bytes"
	"cmp"
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
	"google.golang.org/protobuf/types/known/timestamppb"
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

	initialState := modelStateSearching
	if parsedReference != nil {
		initialState = modelStateLoadingReference
	}

	model := model{
		state:            initialState,
		remote:           remote,
		username:         username,
		token:            token,
		currentOwner:     username,
		spinner:          spinner.New(),
		tableStyles:      tableStyles,
		httpClient:       httpClient,
		help:             help.New(),
		keys:             keys,
		currentReference: parsedReference,
		timeView:         timeViewAbsolute,
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

type timeView int

const (
	timeViewAbsolute timeView = iota
	timeViewRelative
)
const (
	bufBlue = lipgloss.Color("#151fd5")
	bufTeal = lipgloss.Color("#91dffb")

	tuuidWidth = 32
)

type model struct {
	// (Basically) Static
	remote      string
	username    string
	token       string
	tableStyles table.Styles
	spinner     spinner.Model
	httpClient  connect.HTTPClient
	keys        keyMap

	// State - where are we?
	state    modelState
	timeView timeView
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
	moduleTable      table.Model
	commitsTable     table.Model
	commitFilesTable table.Model
	fileViewport     viewport.Model
	searchInput      textinput.Model
	help             help.Model
}

func (m model) Init() tea.Cmd {
	if m.currentReference != nil {
		return m.getResource(m.currentReference)
	}
	return nil
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
			return m, m.listCommits()
		case *modulev1.Resource_Commit:
			m.currentOwner = msg.requestedResource.Owner
			m.currentModule = msg.requestedResource.Module
			m.currentCommit = retrievedResource.Commit.Id
			m.state = modelStateLoadingCommitFileContents
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
		m.currentModules = msg
		if len(m.currentModules) == 0 {
			return m, nil
		}
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
		m.moduleTable = table.New(
			table.WithColumns(columns),
			table.WithRows(m.formatModuleRows(m.currentModules)),
			table.WithFocused(true),
			table.WithHeight(len(m.currentModules)),
			table.WithStyles(m.tableStyles),
		)
		return m, nil

	case commitsMsg:
		m.state = modelStateBrowsingCommits
		m.currentCommits = msg
		if len(m.currentCommits) == 0 {
			return m, nil
		}
		columns := []table.Column{
			// TODO: adjust these dynamically?
			{Title: "ID", Width: tuuidWidth},
			{Title: "Create Time", Width: 19},
			// No need to make this too long - it's not really
			// useful to consumers.
			{Title: "b5 Digest", Width: 9},
			// TODO: What else is useful here?
		}
		m.commitsTable = table.New(
			table.WithColumns(columns),
			table.WithRows(m.formatCommitRows(m.currentCommits)),
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
				return m, m.getModules()
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
				m.currentModule = m.moduleTable.SelectedRow()[1] // module name row
				return m, m.listCommits()
			case modelStateBrowsingCommits:
				if len(m.currentCommits) == 0 {
					// Don't do anything.
					return m, nil
				}
				m.state = modelStateLoadingCommitFileContents
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
				// NOTE: We don't necessarily have the commits
				// list populated, because we may have gone
				// directly to a reference.
				// TODO: Hook this up to caching.
				m.state = modelStateLoadingCommits
				return m, m.listCommits()
			case modelStateBrowsingCommits:
				// NOTE: We don't necessarily have the module
				// list populated, because we may have gone
				// directly to a reference.
				// TODO: Hook this up to caching.
				m.state = modelStateLoadingModules
				return m, m.getModules()
			}
		case key.Matches(msg, m.keys.ToggleTimeView):
			if m.timeView == timeViewAbsolute {
				m.timeView = timeViewRelative
			} else {
				m.timeView = timeViewAbsolute
			}
			m.moduleTable.SetRows(m.formatModuleRows(m.currentModules))
			m.commitsTable.SetRows(m.formatCommitRows(m.currentCommits))
			return m, nil
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
	case modelStateLoadingModules:
		view = m.spinner.View() + " Loading modules"
	case modelStateLoadingCommits:
		view = m.spinner.View() + " Loading commits"
	case modelStateLoadingCommitFileContents:
		view = m.spinner.View() + " Loading commit file contents"
	case modelStateLoadingReference:
		view = m.spinner.View() + " Loading reference"
	case modelStateBrowsingModules:
		header := fmt.Sprintf("Modules (Owner: %s)\n", m.currentOwner)
		view = header
		if len(m.currentModules) == 0 {
			view += fmt.Sprintf("No modules found for owner; use %s to search for another owner", keys.Search.Keys())
		} else {
			view += m.moduleTable.View()
		}
	case modelStateBrowsingCommits:
		header := fmt.Sprintf("Commits (Module: %s/%s)\n", m.currentOwner, m.currentModule)
		view = header
		if len(m.currentCommits) == 0 {
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
			fmt.Sprintf("Commit %s (Module: %s/%s)\n", m.currentCommit, m.currentOwner, m.currentModule),
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

func (m model) formatModuleRows(msg modulesMsg) []table.Row {
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
			m.formatTimestamp(module.CreateTime),
			visibility,
			state,
		}
	}
	return rows
}

func (m model) formatCommitRows(msg commitsMsg) []table.Row {
	rows := make([]table.Row, len(msg))
	for i, commit := range msg {
		rows[i] = table.Row{
			commit.Id,
			m.formatTimestamp(commit.CreateTime),
			fmt.Sprintf("%x", commit.Digest.Value),
		}
	}
	return rows
}

func (m model) formatTimestamp(timestamp *timestamppb.Timestamp) string {
	if m.timeView == timeViewAbsolute {
		return timestamp.AsTime().Format(time.DateTime)
	}
	// AsTime() returns a Go time.Time in UTC; make sure now is in UTC.
	return formatTimeAgo(time.Now().UTC(), timestamp.AsTime())
}

type modulesMsg []*modulev1.Module

func (m model) getModules() tea.Cmd {
	return func() tea.Msg {
		moduleServiceClient := modulev1connect.NewModuleServiceClient(
			m.httpClient,
			"https://"+m.remote,
		)
		request := connect.NewRequest(&modulev1.ListModulesRequest{
			OwnerRefs: []*ownerv1.OwnerRef{
				{
					Value: &ownerv1.OwnerRef_Name{
						Name: m.currentOwner,
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
			"https://"+m.remote,
		)
		request := connect.NewRequest(&modulev1.ListCommitsRequest{
			ResourceRef: &modulev1.ResourceRef{
				Value: &modulev1.ResourceRef_Name_{
					Name: &modulev1.ResourceRef_Name{
						Owner:  m.currentOwner,
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
			"https://"+m.remote,
		)
		request := connect.NewRequest(&modulev1.DownloadRequest{
			Values: []*modulev1.DownloadRequest_Value{
				{
					ResourceRef: &modulev1.ResourceRef{
						Value: &modulev1.ResourceRef_Name_{
							Name: &modulev1.ResourceRef_Name{
								Owner:  m.currentOwner,
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
			"https://"+m.remote,
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
			return errMsg{fmt.Errorf("getting resource: %s", err)}
		}
		if len(response.Msg.Resources) != 1 {
			return errMsg{fmt.Errorf("requested 1 resource, got %v", len(response.Msg.Resources))}
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

// formatTimeAgo returns a string representing an amount of time passed between
// now and timestamp.
func formatTimeAgo(now, timestamp time.Time) string {
	if timestamp.After(now) {
		// ??? - Let's not handle this case yet...
		return "in the future"
	}
	if now.Equal(timestamp) {
		return "now"
	}
	// Handle larger differences first.
	if yearDifference := now.Year() - timestamp.Year(); yearDifference != 0 {
		if yearDifference == 1 {
			return "last year"
		}
		return fmt.Sprintf("%d years ago", yearDifference)
	}
	if monthDifference := now.Month() - timestamp.Month(); monthDifference != 0 {
		if monthDifference == 1 {
			return "last month"
		}
		return fmt.Sprintf("%d months ago", monthDifference)
	}
	if dayDifference := now.Day() - timestamp.Day(); dayDifference != 0 {
		if dayDifference == 1 {
			return "yesterday"
		}
		return fmt.Sprintf("%d days ago", dayDifference)
	}
	// Same date.
	durationAgo := now.Sub(timestamp)
	if durationAgo.Seconds() < 60 {
		return "a few seconds ago"
	}
	if durationAgo.Minutes() < 60 {
		return fmt.Sprintf("%d minutes ago", int(durationAgo.Minutes()))
	}
	return fmt.Sprintf("%d hours ago", int(durationAgo.Hours()))
}

// keyMap defines a set of keybindings. To work for help it must satisfy
// key.Map. It could also very easily be a map[string]key.Binding.
type keyMap struct {
	Up             key.Binding
	Down           key.Binding
	Left           key.Binding
	Right          key.Binding
	Search         key.Binding
	Enter          key.Binding
	Help           key.Binding
	Quit           key.Binding
	ToggleTimeView key.Binding
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
	ToggleTimeView: key.NewBinding(
		key.WithKeys("t"),
		key.WithHelp("t", "toggle time view (absolute / relative)"),
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
		// Always last.
		shortHelp = append(shortHelp, keys.ToggleTimeView)
	case modelStateBrowsingCommits, modelStateBrowsingCommitContents:
		shortHelp = []key.Binding{keys.Up, keys.Down, keys.Left}
		if len(m.currentCommits) == 0 {
			// Can't go Right when no commits exist.
			shortHelp = append(shortHelp, keys.Right)
		}
		// Always last.
		shortHelp = append(shortHelp, keys.ToggleTimeView)
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
		{keys.Search, keys.ToggleTimeView, keys.Help, keys.Quit},
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
