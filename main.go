package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"time"

	"buf.build/gen/go/bufbuild/registry/connectrpc/go/buf/registry/module/v1beta1/modulev1beta1connect"
	modulev1beta1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/module/v1beta1"
	ownerv1 "buf.build/gen/go/bufbuild/registry/protocolbuffers/go/buf/registry/owner/v1"
	"connectrpc.com/connect"
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

type model struct {
	moduleTable table.Model
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	fs := flag.NewFlagSet("buftui", flag.ContinueOnError)
	var (
		hostFlag = fs.String("host", "buf.build", "host")
		userFlag = fs.String("user", "", "user")
	)

	if err := ff.Parse(fs, os.Args[1:]); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	client := modulev1beta1connect.NewModuleServiceClient(
		http.DefaultClient,
		fmt.Sprintf("https://%s", *hostFlag),
	)
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

	req := connect.NewRequest(&modulev1beta1.ListModulesRequest{
		OwnerRefs: []*ownerv1.OwnerRef{
			{
				Value: &ownerv1.OwnerRef_Name{
					Name: moduleOwner,
				},
			},
		},
	})
	req.Header().Set(
		"Authorization",
		"Basic "+base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", login, token))),
	)
	resp, err := client.ListModules(ctx, req)
	if err != nil {
		return fmt.Errorf("listing modules: %s", err)
	}
	columns := []table.Column{
		// TODO: adjust these dynamically?
		{Title: "ID", Width: 12},
		{Title: "Name", Width: 20},
		{Title: "Create Time", Width: 19},
		{Title: "Visibility", Width: 10},
	}
	tableHeight := len(resp.Msg.Modules)
	var rows []table.Row
	if len(resp.Msg.Modules) == 0 {
		rows = append(rows, table.Row{
			"No modules found for user",
		})
		tableHeight = 1
	} else {
		for _, module := range resp.Msg.Modules {
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
	model := initialModel()
	model.moduleTable = table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(tableHeight),
	)
	// Style table
	s := table.DefaultStyles()
	bufBlue := lipgloss.Color("#151fd5")
	bufTeal := lipgloss.Color("#91dffb")
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(bufBlue).
		BorderBottom(true).
		Bold(false)
	s.Selected = s.Selected.
		Foreground(bufTeal).
		Background(bufBlue).
		Bold(false)
	model.moduleTable.SetStyles(s)
	if _, err := tea.NewProgram(model).Run(); err != nil {
		return err
	}
	return nil
}

func initialModel() model {
	return model{}
}

func (m model) Init() tea.Cmd {
	// Just return `nil`, which means "no I/O right now, please."
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	// Is it a key press?
	case tea.KeyMsg:

		// Cool, what was the actual key pressed?
		switch msg.String() {

		// These keys should exit the program.
		case "ctrl+c", "q":
			return m, tea.Quit
		case "enter":
			return m, tea.Batch(
				// TODO: Go to commits view for module.
				tea.Printf("Let's go to %s!", m.moduleTable.SelectedRow()[1]),
			)
		}
	}
	var cmd tea.Cmd
	m.moduleTable, cmd = m.moduleTable.Update(msg)
	return m, cmd
}

func (m model) View() string {
	return m.moduleTable.View()
}
