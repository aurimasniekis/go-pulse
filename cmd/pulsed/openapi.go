package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/danielgtaylor/huma/v2"
	"github.com/spf13/cobra"

	"github.com/aurimasniekis/go-pulse/internal/api"
	"github.com/aurimasniekis/go-pulse/internal/app"
)

// specs builds both OpenAPI documents without any running services (PL-API-002).
func specs() map[string]*huma.OpenAPI {
	d := &api.Deps{Version: app.Version}
	return map[string]*huma.OpenAPI{
		"admin":  api.NewAdminAPI(http.NewServeMux(), d).OpenAPI(),
		"public": api.NewPublicAPI(http.NewServeMux(), d).OpenAPI(),
	}
}

func newOpenAPICmd() *cobra.Command {
	var outDir, which string
	cmd := &cobra.Command{
		Use:   "openapi",
		Short: "Print the OpenAPI 3.1 specs (admin and public), or write them to --out-dir",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			all := specs()
			for name, spec := range all {
				if which != "" && which != name {
					continue
				}
				// Keep generated files stable across builds.
				spec.Info.Version = "v1"
				b, err := json.MarshalIndent(spec, "", "  ")
				if err != nil {
					return err
				}
				b = append(b, '\n')
				if outDir == "" {
					if _, err := cmd.OutOrStdout().Write(b); err != nil {
						return err
					}
					continue
				}
				p := filepath.Join(outDir, name+".openapi.json")
				if err := os.WriteFile(p, b, 0o644); err != nil { //nolint:gosec // public spec file
					return err
				}
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "wrote", p)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&outDir, "out-dir", "", "write <name>.openapi.json files here")
	cmd.Flags().StringVar(&which, "api", "", "admin or public (default: both)")
	return cmd
}
