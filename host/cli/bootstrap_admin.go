package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/term"
)

type bootstrapAdmin struct {
	Email    string
	Handle   string
	Password string
}

func readBootstrapAdmin(args *docopt.Args) (*bootstrapAdmin, error) {
	return readBootstrapAdminMode(args, term.IsTerminal(os.Stdin.Fd()))
}

func readBootstrapAdminMode(args *docopt.Args, interactive bool) (*bootstrapAdmin, error) {
	email := strings.TrimSpace(strings.ToLower(args.String["--admin-email"]))
	password := args.String["--admin-password"]
	if email == "" {
		if !interactive {
			return nil, fmt.Errorf("--admin-email is required for non-interactive bootstrap")
		}
		fmt.Fprint(os.Stderr, "Administrator email: ")
		if _, err := fmt.Scanln(&email); err != nil {
			return nil, fmt.Errorf("admin email: %w", err)
		}
		email = strings.TrimSpace(strings.ToLower(email))
	}
	if email == "" || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("administrator email is required")
	}
	handle := tenancy.HandleFromEmail(email)
	if password == "" {
		if !interactive {
			return nil, fmt.Errorf("--admin-password is required for non-interactive bootstrap")
		}
		fmt.Fprint(os.Stderr, "Administrator password: ")
		if _, err := fmt.Scanln(&password); err != nil {
			return nil, fmt.Errorf("admin password: %w", err)
		}
		var confirm string
		fmt.Fprint(os.Stderr, "Confirm password: ")
		if _, err := fmt.Scanln(&confirm); err != nil {
			return nil, fmt.Errorf("confirm password: %w", err)
		}
		if password != confirm {
			return nil, fmt.Errorf("passwords do not match")
		}
	}
	if strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("administrator password is required")
	}
	return &bootstrapAdmin{Email: email, Handle: handle, Password: password}, nil
}

func createBootstrapAdmin(admin *bootstrapAdmin) error {
	if admin == nil {
		return nil
	}
	v, err := hostV1()
	if err != nil {
		return fmt.Errorf("create bootstrap admin: %w", err)
	}
	body := ct.UserCreate{Email: admin.Email, Handle: admin.Handle, Password: admin.Password, ClusterAdmin: true}
	var u ct.User
	if err := v.Post("/users", body, &u); err != nil {
		return fmt.Errorf("create bootstrap admin: %w", err)
	}
	fmt.Printf("created cluster administrator %s\n", u.Email)
	return nil
}
