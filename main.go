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
	modelStateLoadingModules  modelState = "loading-modules"
	modelStateBrowsingModules modelState = "browsing-modules"
	modelStateLoadingCommits  modelState = "loading-commits"
	modelStateBrowsingCommits modelState = "browsing-commits"
)

type model struct {
	state modelState

	spinner spinner.Model

	moduleTable   table.Model
	commitsTable  table.Model
	currentModule string

	hostname    string
	login       string
	token       string
	moduleOwner string

	err error

	tableStyles table.Styles
}

var (
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
		hostFlag = fs.String("host", "buf.build", "host")
		userFlag = fs.String("user", "", "user")
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

	model := model{
		state:       modelStateLoadingModules,
		hostname:    *hostFlag,
		login:       login,
		token:       token,
		moduleOwner: moduleOwner,
		spinner:     spinner.New(),
		tableStyles: tableStyles,
	}

	if _, err := tea.NewProgram(model).Run(); err != nil {
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

	case errMsg:
		m.err = msg.err

	// Is it a key press?
	case tea.KeyMsg:

		// Cool, what was the actual key pressed?
		switch msg.String() {

		// These keys should exit the program.
		case "ctrl+c", "q":
			return m, tea.Quit
		case "enter":
			switch m.state {
			case modelStateBrowsingModules:
				m.state = modelStateLoadingCommits
				m.currentModule = m.moduleTable.SelectedRow()[1]
				return m, m.getCommits()
			default:
				// Don't do anything, yet, in other states.
			}
		}
	}
	var cmd tea.Cmd
	m.moduleTable, cmd = m.moduleTable.Update(msg)
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
		return m.moduleTable.View()
	}
	return fmt.Sprintf("unaccounted state: %v", m.state)
}

type modulesMsg []*modulev1beta1.Module

func (m model) getModules() tea.Cmd {
	return func() tea.Msg {
		client := httplb.NewClient()
		defer client.Close()
		moduleServiceClient := modulev1beta1connect.NewModuleServiceClient(
			client,
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := moduleServiceClient.ListModules(ctx, req)
		if err != nil {
			return errMsg{fmt.Errorf("listing modules: %s", err)}
		}
		return modulesMsg(resp.Msg.Modules)
	}
}

type commitsMsg []*modulev1beta1.Commit

func (m model) getCommits() tea.Cmd {
	return func() tea.Msg {
		client := httplb.NewClient()
		defer client.Close()
		commitServiceClient := modulev1beta1connect.NewCommitServiceClient(
			client,
			fmt.Sprintf("https://%s", m.hostname),
		)
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
		})
		req.Header().Set(
			"Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", m.login, m.token))),
		)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		resp, err := commitServiceClient.GetCommits(ctx, req)
		if err != nil {
			return errMsg{fmt.Errorf("getting commits: %s", err)}
		}
		return commitsMsg(resp.Msg.Commits)
	}
}

type errMsg struct{ err error }

// For messages that contain errors it's often handy to also implement the
// error interface on the message.
func (e errMsg) Error() string { return e.err.Error() }
