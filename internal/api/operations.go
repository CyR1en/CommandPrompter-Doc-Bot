package api

import (
	"context"
	"net/http"
	"reflect"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/operations"
	"github.com/danielgtaylor/huma/v2"
)

const (
	overviewPath = "/api/v1/overview"
	exportPath   = "/api/v1/settings/export"
)

type operationsService interface {
	Overview(context.Context) (operations.OperationalOverview, error)
	ExportConfiguration(context.Context) (operations.ConfigurationExport, error)
}

type operationsInput struct {
	SessionCookie string `cookie:"ref0_session"`
}

type overviewOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         operations.OperationalOverview
}

type configurationExportOutput struct {
	CacheControl       string `header:"Cache-Control"`
	ContentDisposition string `header:"Content-Disposition"`
	ContentTypeOptions string `header:"X-Content-Type-Options"`
	Body               operations.ConfigurationExport
}

func registerOperations(api huma.API, sessions auth.SessionService, service operationsService) {
	huma.Register(api, huma.Operation{
		OperationID: "operational_overview_api_v1_overview_get",
		Method:      http.MethodGet,
		Path:        overviewPath,
		Summary:     "Operational Overview",
		Tags:        []string{"operations"},
		Errors:      []int{http.StatusUnauthorized},
	}, func(ctx context.Context, input *operationsInput) (*overviewOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, overviewPath); err != nil {
			return nil, err
		}
		value, err := service.Overview(ctx)
		if err != nil {
			return nil, operationsProblem(overviewPath)
		}
		return &overviewOutput{CacheControl: "no-store", Body: value}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "export_configuration_api_v1_settings_export_get",
		Method:      http.MethodGet,
		Path:        exportPath,
		Summary:     "Export Configuration",
		Tags:        []string{"operations"},
		Errors:      []int{http.StatusUnauthorized},
	}, func(ctx context.Context, input *operationsInput) (*configurationExportOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, exportPath); err != nil {
			return nil, err
		}
		value, err := service.ExportConfiguration(ctx)
		if err != nil {
			return nil, operationsProblem(exportPath)
		}
		return &configurationExportOutput{
			CacheControl:       "no-store",
			ContentDisposition: `attachment; filename="ref0-configuration.json"`,
			ContentTypeOptions: "nosniff",
			Body:               value,
		}, nil
	})
	normalizeOperationsOpenAPISchemas(api)
}

func normalizeOperationsOpenAPISchemas(api huma.API) {
	registry := api.OpenAPI().Components.Schemas
	schemas := registry.Map()
	export := schemas["ConfigurationExport"]
	if export != nil {
		for _, name := range []string{
			"redacted_fields", "credentials", "knowledge_bases", "sources", "providers", "models",
			"model_assignments", "agents", "discord_connections", "discord_bindings",
		} {
			if property := export.Properties[name]; property != nil {
				property.Nullable = false
			}
		}
		if sources := export.Properties["sources"]; sources != nil {
			sources.Items = &huma.Schema{AnyOf: []*huma.Schema{
				registry.Schema(reflect.TypeFor[operations.RepositorySourceConfiguration](), true, "RepositorySourceConfiguration"),
				registry.Schema(reflect.TypeFor[operations.WebsiteSourceConfiguration](), true, "WebsiteSourceConfiguration"),
			}}
		}
	}
	for schemaName, fields := range map[string][]string{
		"RepositorySourceConfiguration": {"include_patterns", "exclude_patterns"},
		"ProviderConfiguration":         {"header_names"},
		"DiscordBindingConfiguration":   {"allowed_role_ids", "allowed_user_ids"},
		"OperationalOverview": {
			"unhealthy_sources", "failed_jobs", "knowledge_base_issues", "provider_errors", "agent_failures",
		},
	} {
		if schema := schemas[schemaName]; schema != nil {
			for _, field := range fields {
				if property := schema.Properties[field]; property != nil {
					property.Nullable = false
				}
			}
		}
	}
}

func operationsProblem(instance string) error {
	return &apiProblem{
		Type:     "about:blank",
		Title:    "Internal Server Error",
		Status:   http.StatusInternalServerError,
		Detail:   "The request could not be completed.",
		Instance: instance,
	}
}
