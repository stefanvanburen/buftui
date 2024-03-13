package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"buf.build/gen/go/bufbuild/registry/connectrpc/go/buf/registry/module/v1beta1/modulev1beta1connect"
	modulev1beta1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1beta1"
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	"connectrpc.com/connect"
	"github.com/bufbuild/httplb"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jdx/go-netrc"
	"github.com/peterbourgon/ff/v4"
)

// TODO:
// * search for user / organization
// * module view
// * commit view
// * docs view?

type modelState string

const (
	modelStateLoadingModules             modelState = "loading-modules"
	modelStateBrowsingModules            modelState = "browsing-modules"
	modelStateLoadingCommits             modelState = "loading-commits"
	modelStateBrowsingCommits            modelState = "browsing-commits"
	modelStateLoadingCommitContents      modelState = "loading-commit-contents"
	modelStateBrowsingCommitContents     modelState = "browsing-commit-contents"
	modelStateBrowsingCommitFileContents modelState = "browsing-commit-file-contents"
)

type model struct {
	state modelState

	spinner spinner.Model

	moduleTable        table.Model
	commitsTable       table.Model
	commitFilesTable   table.Model
	currentModule      string
	currentCommit      string
	currentCommitFiles []*modulev1beta1.File
	fileViewport       viewport.Model

	hostname    string
	login       string
	token       string
	moduleOwner string

	err error

	tableStyles table.Styles

	httpClient connect.HTTPClient
}

const (
	bufBlue = lipgloss.Color("#151fd5")
	bufTeal = lipgloss.Color("#91dffb")
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}

func run(_ context.Context) error {
	fs := flag.NewFlagSet("buftui", flag.ContinueOnError)
	var (
		hostFlag       = fs.String("host", "buf.build", "host")
		userFlag       = fs.String("user", "", "user")
		fullscreenFlag = fs.Bool("fullscreen", false, "Enable fullscreen display")
	)

	if err := ff.Parse(fs, os.Args[1:]); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	usr, err := user.Current()
	if err != nil {
		return fmt.Errorf("getting current user: %s", err)
	}
	n, err := netrc.Parse(filepath.Join(usr.HomeDir, ".netrc"))
	if err != nil {
		return fmt.Errorf("parsing netrc: %s", err)
	}
	// TODO: What should we do with neither set, or just one set?
	login := n.Machine(*hostFlag).Get("login")
	token := n.Machine(*hostFlag).Get("password")
	moduleOwner := login
	if *userFlag != "" {
		moduleOwner = *userFlag
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
		state:       modelStateLoadingModules,
		hostname:    *hostFlag,
		login:       login,
		token:       token,
		moduleOwner: moduleOwner,
		spinner:     spinner.New(),
		tableStyles: tableStyles,
		httpClient:  httpClient,
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
	return m.getModules()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case modulesMsg:
		columns := []table.Column{
			// TODO: adjust these dynamically?
			{Title: "ID", Width: 12},
			{Title: "Name", Width: 20},
			{Title: "Create Time", Width: 19},
			{Title: "Visibility", Width: 10},
		}
		tableHeight := len(msg)
		var rows []table.Row
		if len(msg) == 0 {
			rows = append(rows, table.Row{
				"No modules found for user",
			})
			tableHeight = 1
		} else {
			for _, module := range msg {
				var visibility string
				switch module.Visibility {
				case modulev1beta1.ModuleVisibility_MODULE_VISIBILITY_PRIVATE:
					visibility = "private"
				case modulev1beta1.ModuleVisibility_MODULE_VISIBILITY_PUBLIC:
					visibility = "public"
				default:
					visibility = "unknown"
				}
				rows = append(rows, table.Row{
					module.Id,
					module.Name,
					module.CreateTime.AsTime().Format(time.DateTime),
					visibility,
				})
			}
		}
		m.moduleTable = table.New(
			table.WithColumns(columns),
			table.WithRows(rows),
			table.WithFocused(true),
			table.WithHeight(tableHeight),
			table.WithStyles(m.tableStyles),
		)
		m.state = modelStateBrowsingModules
		return m, nil

	case commitsMsg:
		columns := []table.Column{
			// TODO: adjust these dynamically?
			{Title: "ID", Width: 12},
			{Title: "Create Time", Width: 19},
			// TODO: What makes sense?
			{Title: "Digest", Width: 19},
			// TODO: What else is useful here?
		}
		tableHeight := len(msg)
		var rows []table.Row
		if len(msg) == 0 {
			rows = append(rows, table.Row{
				"No commits found for module",
			})
			tableHeight = 1
		} else {
			for _, commit := range msg {
				rows = append(rows, table.Row{
					commit.Id,
					commit.CreateTime.AsTime().Format(time.DateTime),
					fmt.Sprintf("%s:%s", commit.Digest.Type.String(), commit.Digest.Value),
				})
			}
		}
		m.commitsTable = table.New(
			table.WithColumns(columns),
			table.WithRows(rows),
			table.WithFocused(true),
			table.WithHeight(tableHeight),
			table.WithStyles(m.tableStyles),
		)
		m.state = modelStateBrowsingCommits
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
				rows[i] = table.Row{
					file.Path,
					// string(file.Content),
				}
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
		m.fileViewport = viewport.New(80, tableHeight)
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
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "enter", "l":
			switch m.state {
			case modelStateBrowsingModules:
				m.state = modelStateLoadingCommits
				m.currentModule = m.moduleTable.SelectedRow()[1] // module name row
				return m, m.getCommits()
			case modelStateBrowsingCommits:
				m.state = modelStateLoadingCommitContents
				m.currentCommit = m.commitsTable.SelectedRow()[0] // commit name row
				return m, m.getCommitContent(m.currentCommit)
			case modelStateBrowsingCommitContents:
				m.state = modelStateBrowsingCommitFileContents
			default:
				// Don't do anything, yet, in other states.
			}
		case "h":
			// "h" -> "Go out"
			switch m.state {
			case modelStateBrowsingCommitFileContents:
				m.state = modelStateBrowsingCommitContents
			case modelStateBrowsingCommitContents:
				m.state = modelStateBrowsingCommits
			case modelStateBrowsingCommits:
				m.state = modelStateBrowsingModules
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
		for _, file := range m.currentCommitFiles {
			if file.Path == m.commitFilesTable.SelectedRow()[0] {
				m.fileViewport.SetContent(string(file.Content))
			}
		}
	case modelStateBrowsingCommitFileContents:
		m.fileViewport, cmd = m.fileViewport.Update(msg)
	}
	return m, cmd
}

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("error: %v", m.err)
	}
	switch m.state {
	case modelStateLoadingModules:
		return m.spinner.View()
	case modelStateBrowsingModules:
		return m.moduleTable.View()
	case modelStateLoadingCommits:
		return m.spinner.View()
	case modelStateBrowsingCommits:
		return m.commitsTable.View()
	case modelStateLoadingCommitContents:
		return m.spinner.View()
	case modelStateBrowsingCommitContents, modelStateBrowsingCommitFileContents:
		fileView := m.fileViewport.View()
		if m.state == modelStateBrowsingCommitFileContents {
			fileViewStyle := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder(), true).
				BorderForeground(bufBlue)
			fileView = fileViewStyle.Render(fileView)
		}
		return lipgloss.JoinHorizontal(
			lipgloss.Left,
			m.commitFilesTable.View(),
			fileView,
		)
	}
	return fmt.Sprintf("unaccounted state: %v", m.state)
}

type modulesMsg []*modulev1beta1.Module

func (m model) getModules() tea.Cmd {
	return func() tea.Msg {
		moduleServiceClient := modulev1beta1connect.NewModuleServiceClient(
			m.httpClient,
			fmt.Sprintf("https://%s", m.hostname),
		)
		req := connect.NewRequest(&modulev1beta1.ListModulesRequest{
			OwnerRefs: []*ownerv1.OwnerRef{
				{
					Value: &ownerv1.OwnerRef_Name{
						Name: m.moduleOwner,
					},
				},
			},
		})
		req.Header().Set(
			"Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", m.login, m.token))),
		)
		resp, err := moduleServiceClient.ListModules(context.Background(), req)
		if err != nil {
			return errMsg{fmt.Errorf("listing modules: %s", err)}
		}
		return modulesMsg(resp.Msg.Modules)
	}
}

type commitsMsg []*modulev1beta1.Commit

func (m model) getCommits() tea.Cmd {
	return func() tea.Msg {
		commitServiceClient := modulev1beta1connect.NewCommitServiceClient(
			m.httpClient,
			fmt.Sprintf("https://%s", m.hostname),
		)
		// TODO: This only supports getting a single commit per
		// reference; we ideally want a list of commits.
		// Change this to `ListCommits`, once it's implemented.
		req := connect.NewRequest(&modulev1beta1.GetCommitsRequest{
			ResourceRefs: []*modulev1beta1.ResourceRef{
				{
					Value: &modulev1beta1.ResourceRef_Name_{
						Name: &modulev1beta1.ResourceRef_Name{
							Owner:  m.moduleOwner,
							Module: m.currentModule,
						},
					},
				},
			},
			// TODO: Switch to B5 when supported.
			DigestType: modulev1beta1.DigestType_DIGEST_TYPE_B4,
		})
		req.Header().Set(
			"Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", m.login, m.token))),
		)
		resp, err := commitServiceClient.GetCommits(context.Background(), req)
		if err != nil {
			return errMsg{fmt.Errorf("getting commits: %s", err)}
		}
		return commitsMsg(resp.Msg.Commits)
	}
}

type contentsMsg *modulev1beta1.DownloadResponse_Content

func (m model) getCommitContent(commitName string) tea.Cmd {
	return func() tea.Msg {
		client := httplb.NewClient()
		defer client.Close()
		commitServiceClient := modulev1beta1connect.NewDownloadServiceClient(
			m.httpClient,
			fmt.Sprintf("https://%s", m.hostname),
		)
		req := connect.NewRequest(&modulev1beta1.DownloadRequest{
			Values: []*modulev1beta1.DownloadRequest_Value{
				{
					ResourceRef: &modulev1beta1.ResourceRef{
						Value: &modulev1beta1.ResourceRef_Name_{
							Name: &modulev1beta1.ResourceRef_Name{
								Owner:  m.moduleOwner,
								Module: m.currentModule,
								Child: &modulev1beta1.ResourceRef_Name_Ref{
									Ref: commitName,
								},
							},
						},
					},
				},
			},
			DigestType: modulev1beta1.DigestType_DIGEST_TYPE_B4,
		})
		req.Header().Set(
			"Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", m.login, m.token))),
		)
		resp, err := commitServiceClient.Download(context.Background(), req)
		if err != nil {
			return errMsg{fmt.Errorf("getting commit content: %s", err)}
		}
		if len(resp.Msg.Contents) != 1 {
			return errMsg{fmt.Errorf("requested 1 commit contents, got %v", len(resp.Msg.Contents))}
		}
		return contentsMsg(resp.Msg.Contents[0])
	}
}

type errMsg struct{ err error }

// For messages that contain errors it's often handy to also implement the
// error interface on the message.
func (e errMsg) Error() string { return e.err.Error() }
