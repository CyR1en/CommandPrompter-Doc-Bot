package api

import (
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/danielgtaylor/huma/v2"
)

// controlPlaneRoutes keeps HTTP registration separate from process
// construction. Tests can exercise the base handler with small fakes, while
// the production entrypoint installs every persisted control-plane slice.
type controlPlaneRoutes struct {
	sessions          auth.SessionService
	credentials       credentialService
	knowledgeBases    knowledgeBaseService
	providers         ProviderService
	sources           SourceService
	discord           DiscordService
	jobs              DiscordJobReader
	documentation     DocumentationService
	documentationJobs DocumentationJobService
	agents            agentService
	chatTokens        chatTokenService
}

func (routes controlPlaneRoutes) register(api huma.API) {
	registerCredentials(api, routes.sessions, routes.credentials)
	registerKnowledgeBases(api, routes.sessions, routes.knowledgeBases)
	RegisterProviderRoutes(api, routes.sessions, routes.providers)
	RegisterSourceRoutes(api, routes.sessions, routes.sources)
	RegisterDiscordRoutes(api, routes.sessions, routes.discord, routes.jobs)
	RegisterDocumentationRoutes(api, routes.sessions, routes.documentation, routes.documentationJobs)
	registerAgents(api, routes.sessions, routes.agents)
	registerChatTokens(api, routes.sessions, routes.chatTokens, routes.agents)
}
