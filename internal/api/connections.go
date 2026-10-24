package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-pulse/internal/sysapi"
)

// Connections is the server-wide connection list for the System page.
type Connections struct {
	Total       int       `json:"total"`
	Connections []ConnDTO `json:"connections"`
}

func registerConnections(api huma.API, d *Deps) {
	huma.Register(api, huma.Operation{OperationID: "list-connections", Method: http.MethodGet, Path: "/system/connections",
		Summary: "NATS connections across accounts, busiest first", Tags: []string{"system"}},
		func(ctx context.Context, in *struct {
			Sort  string `query:"sort" enum:"msgs_to,msgs_from,bytes_to,bytes_from,pending,start,last" default:"msgs_to"`
			Limit int    `query:"limit" minimum:"1" maximum:"500" default:"100"`
		}) (*struct{ Body Connections }, error) {
			cz, err := d.Sys.Connz(ctx, sysapi.ConnzOptions{Auth: true, Sort: in.Sort, Limit: in.Limit})
			if err != nil {
				return nil, d.internalErr("list-connections", err)
			}
			out := Connections{Total: cz.Total, Connections: []ConnDTO{}}
			names := d.accountNames()
			for _, c := range cz.Conns {
				dto := connDTO(c)
				dto.Account = names[c.Account]
				if dto.Account == "" {
					dto.Account = c.Account
				}
				out.Connections = append(out.Connections, dto)
			}
			return &struct{ Body Connections }{out}, nil
		})
}
