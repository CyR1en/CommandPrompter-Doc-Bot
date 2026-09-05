-- +goose Up

--
-- PostgreSQL database dump
--

-- Dumped from database version 18.6 (Debian 18.6-1.pgdg12+2)
-- Dumped by pg_dump version 18.6 (Debian 18.6-1.pgdg12+2)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: agent_run_knowledge_bases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_run_knowledge_bases (
    run_id uuid NOT NULL,
    "position" integer NOT NULL,
    knowledge_base_id uuid NOT NULL,
    knowledge_base_version integer NOT NULL,
    access_policy character varying(16) NOT NULL,
    wiki_version_id uuid NOT NULL,
    documentation_run_id uuid NOT NULL,
    source_revision_ids uuid[] DEFAULT '{}'::uuid[] NOT NULL,
    source_scope_digest bytea NOT NULL,
    CONSTRAINT ck_agent_run_knowledge_bases_access_policy_valid CHECK (((access_policy)::text = ANY ((ARRAY['PUBLIC'::character varying, 'RESTRICTED'::character varying])::text[]))),
    CONSTRAINT ck_agent_run_knowledge_bases_knowledge_base_version_positive CHECK ((knowledge_base_version > 0)),
    CONSTRAINT ck_agent_run_knowledge_bases_position_valid CHECK ((("position" >= 0) AND ("position" < 32))),
    CONSTRAINT ck_agent_run_knowledge_bases_source_revision_ids_valid CHECK (((cardinality(source_revision_ids) <= 1024) AND (array_position(source_revision_ids, NULL::uuid) IS NULL))),
    CONSTRAINT ck_agent_run_knowledge_bases_source_scope_digest_length CHECK ((octet_length(source_scope_digest) = 32))
);


--
-- Name: agent_run_scope_reservations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_run_scope_reservations (
    run_id uuid NOT NULL,
    "position" integer NOT NULL,
    knowledge_base_id uuid NOT NULL,
    wiki_version_id uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_agent_run_scope_reservations_expiry_valid CHECK ((expires_at > created_at)),
    CONSTRAINT ck_agent_run_scope_reservations_position_valid CHECK ((("position" >= 0) AND ("position" < 32)))
);


--
-- Name: agent_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    agent_id uuid NOT NULL,
    agent_version_id uuid NOT NULL,
    agent_resource_version integer NOT NULL,
    agent_version_number integer NOT NULL,
    model_profile_id uuid NOT NULL,
    model_profile_version_id uuid NOT NULL,
    model_profile_version_number integer NOT NULL,
    provider_endpoint_id uuid NOT NULL,
    captured_endpoint_configuration_version integer NOT NULL,
    captured_credential_id uuid,
    captured_credential_version integer,
    origin character varying(16) NOT NULL,
    subject character varying(255) NOT NULL,
    request_digest bytea NOT NULL,
    effective_access_policy character varying(16) NOT NULL,
    outcome character varying(32) NOT NULL,
    model_usage jsonb DEFAULT '{}'::jsonb NOT NULL,
    latency_ms integer NOT NULL,
    tool_calls jsonb DEFAULT '[]'::jsonb NOT NULL,
    citations jsonb DEFAULT '[]'::jsonb NOT NULL,
    sanitized_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_agent_runs_agent_versions_positive CHECK (((agent_resource_version > 0) AND (agent_version_number > 0))),
    CONSTRAINT ck_agent_runs_captured_credential_valid CHECK ((((captured_credential_id IS NULL) AND (captured_credential_version IS NULL)) OR ((captured_credential_id IS NOT NULL) AND (captured_credential_version > 0)))),
    CONSTRAINT ck_agent_runs_completed_after_creation CHECK ((completed_at >= created_at)),
    CONSTRAINT ck_agent_runs_effective_access_policy_valid CHECK (((effective_access_policy)::text = ANY ((ARRAY['PUBLIC'::character varying, 'RESTRICTED'::character varying])::text[]))),
    CONSTRAINT ck_agent_runs_endpoint_configuration_version_positive CHECK ((captured_endpoint_configuration_version > 0)),
    CONSTRAINT ck_agent_runs_failure_error_valid CHECK ((((outcome)::text = 'FAILED'::text) = (sanitized_error IS NOT NULL))),
    CONSTRAINT ck_agent_runs_latency_nonnegative CHECK ((latency_ms >= 0)),
    CONSTRAINT ck_agent_runs_model_profile_version_positive CHECK ((model_profile_version_number > 0)),
    CONSTRAINT ck_agent_runs_model_usage_valid CHECK (((jsonb_typeof(model_usage) = 'object'::text) AND (octet_length((model_usage)::text) <= 65536))),
    CONSTRAINT ck_agent_runs_origin_valid CHECK (((origin)::text = ANY ((ARRAY['HTTP'::character varying, 'DISCORD'::character varying])::text[]))),
    CONSTRAINT ck_agent_runs_outcome_valid CHECK (((outcome)::text = ANY ((ARRAY['ANSWERED'::character varying, 'REFUSED'::character varying, 'INSUFFICIENT_EVIDENCE'::character varying, 'FAILED'::character varying])::text[]))),
    CONSTRAINT ck_agent_runs_request_digest_length CHECK ((octet_length(request_digest) = 32)),
    CONSTRAINT ck_agent_runs_sanitized_error_bounded CHECK (((sanitized_error IS NULL) OR (length(sanitized_error) <= 1000))),
    CONSTRAINT ck_agent_runs_subject_valid CHECK (((length(subject) >= 1) AND (length(subject) <= 255) AND ((subject)::text = btrim((subject)::text)))),
    CONSTRAINT ck_agent_runs_tool_and_citation_json_valid CHECK (((jsonb_typeof(tool_calls) = 'array'::text) AND (jsonb_array_length(tool_calls) <= 256) AND (octet_length((tool_calls)::text) <= 262144) AND (jsonb_typeof(citations) = 'array'::text) AND (jsonb_array_length(citations) <= 256) AND (octet_length((citations)::text) <= 262144)))
);


--
-- Name: agent_version_knowledge_bases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_version_knowledge_bases (
    agent_id uuid NOT NULL,
    agent_version_id uuid NOT NULL,
    "position" integer NOT NULL,
    knowledge_base_id uuid NOT NULL,
    CONSTRAINT ck_agent_version_knowledge_bases_position_valid CHECK ((("position" >= 0) AND ("position" < 32)))
);


--
-- Name: agent_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agent_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    agent_id uuid NOT NULL,
    version_number integer NOT NULL,
    display_name character varying(255) NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    response_language character varying(35) NOT NULL,
    identity_instructions text NOT NULL,
    model_profile_id uuid NOT NULL,
    reasoning_effort character varying(16) NOT NULL,
    answer_mode character varying(16) NOT NULL,
    behavioral_instructions text DEFAULT ''::text NOT NULL,
    evidence_access character varying(32) NOT NULL,
    refusal_markdown text NOT NULL,
    max_tool_calls integer NOT NULL,
    max_answer_tokens integer NOT NULL,
    created_by_operator_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_agent_versions_answer_mode_valid CHECK (((answer_mode)::text = ANY ((ARRAY['TOOL_CALLING'::character varying, 'SINGLE_PASS'::character varying])::text[]))),
    CONSTRAINT ck_agent_versions_behavioral_instructions_bounded CHECK ((length(behavioral_instructions) <= 16000)),
    CONSTRAINT ck_agent_versions_description_bounded CHECK ((length(description) <= 2000)),
    CONSTRAINT ck_agent_versions_display_name_valid CHECK (((length(display_name) >= 1) AND (length(display_name) <= 255) AND ((display_name)::text = btrim((display_name)::text)))),
    CONSTRAINT ck_agent_versions_evidence_access_valid CHECK (((evidence_access)::text = ANY ((ARRAY['WIKI_ONLY'::character varying, 'WIKI_AND_SOURCE'::character varying])::text[]))),
    CONSTRAINT ck_agent_versions_identity_instructions_valid CHECK (((length(identity_instructions) >= 1) AND (length(identity_instructions) <= 16000) AND (identity_instructions = btrim(identity_instructions)))),
    CONSTRAINT ck_agent_versions_max_answer_tokens_valid CHECK (((max_answer_tokens >= 1) AND (max_answer_tokens <= 262144))),
    CONSTRAINT ck_agent_versions_max_tool_calls_valid CHECK (((max_tool_calls >= 0) AND (max_tool_calls <= 64))),
    CONSTRAINT ck_agent_versions_mode_tool_limit_valid CHECK (((((answer_mode)::text = 'SINGLE_PASS'::text) AND (max_tool_calls = 0)) OR (((answer_mode)::text = 'TOOL_CALLING'::text) AND (max_tool_calls > 0)))),
    CONSTRAINT ck_agent_versions_reasoning_effort_valid CHECK (((reasoning_effort)::text = ANY ((ARRAY['NONE'::character varying, 'MINIMAL'::character varying, 'LOW'::character varying, 'MEDIUM'::character varying, 'HIGH'::character varying, 'MAX'::character varying])::text[]))),
    CONSTRAINT ck_agent_versions_refusal_markdown_valid CHECK (((length(refusal_markdown) >= 1) AND (length(refusal_markdown) <= 4000) AND (refusal_markdown = btrim(refusal_markdown)))),
    CONSTRAINT ck_agent_versions_response_language_valid CHECK (((length(response_language) >= 1) AND (length(response_language) <= 35) AND ((response_language)::text = btrim((response_language)::text)))),
    CONSTRAINT ck_agent_versions_source_access_mode_valid CHECK ((((evidence_access)::text <> 'WIKI_AND_SOURCE'::text) OR ((answer_mode)::text = 'TOOL_CALLING'::text))),
    CONSTRAINT ck_agent_versions_version_number_positive CHECK ((version_number > 0))
);


--
-- Name: agents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.agents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    agent_key character varying(64) NOT NULL,
    lifecycle character varying(16) DEFAULT 'DRAFT'::character varying NOT NULL,
    current_version_id uuid NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    activated_at timestamp with time zone,
    archived_at timestamp with time zone,
    CONSTRAINT ck_agents_agent_key_valid CHECK (((agent_key)::text ~ '^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$'::text)),
    CONSTRAINT ck_agents_lifecycle_state_valid CHECK (((((lifecycle)::text = 'DRAFT'::text) AND (activated_at IS NULL) AND (archived_at IS NULL)) OR (((lifecycle)::text = 'ACTIVE'::text) AND (activated_at IS NOT NULL) AND (archived_at IS NULL)) OR (((lifecycle)::text = 'ARCHIVED'::text) AND (archived_at IS NOT NULL)))),
    CONSTRAINT ck_agents_lifecycle_valid CHECK (((lifecycle)::text = ANY ((ARRAY['DRAFT'::character varying, 'ACTIVE'::character varying, 'ARCHIVED'::character varying])::text[]))),
    CONSTRAINT ck_agents_timestamps_ordered CHECK (((updated_at >= created_at) AND ((activated_at IS NULL) OR (activated_at >= created_at)) AND ((archived_at IS NULL) OR (archived_at >= created_at)))),
    CONSTRAINT ck_agents_version_positive CHECK ((version > 0))
);


--
-- Name: audit_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    actor_type character varying(32) NOT NULL,
    actor_id uuid,
    action character varying(128) NOT NULL,
    target_type character varying(64) NOT NULL,
    target_id uuid,
    request_id uuid NOT NULL,
    details jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_audit_events_details_object CHECK ((jsonb_typeof(details) = 'object'::text))
);


--
-- Name: artifact_deletion_intents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.artifact_deletion_intents (
    kind character varying(32) NOT NULL,
    resource_id uuid NOT NULL,
    owner_id uuid NOT NULL,
    scope_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_artifact_deletion_intents_kind_valid CHECK (((kind)::text = ANY ((ARRAY['WIKI_VERSION'::character varying, 'FAILED_DRAFT'::character varying, 'SOURCE_SNAPSHOT'::character varying, 'KNOWLEDGE_BASE'::character varying])::text[])))
);


--
-- Name: bootstrap_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.bootstrap_tokens (
    id smallint DEFAULT 1 NOT NULL,
    token_digest bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    CONSTRAINT ck_bootstrap_tokens_consumed_after_creation CHECK (((consumed_at IS NULL) OR (consumed_at >= created_at))),
    CONSTRAINT ck_bootstrap_tokens_expiry_after_creation CHECK ((expires_at > created_at)),
    CONSTRAINT ck_bootstrap_tokens_singleton CHECK ((id = 1)),
    CONSTRAINT ck_bootstrap_tokens_token_digest_length CHECK ((octet_length(token_digest) = 32))
);


--
-- Name: chat_access_token_agents; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.chat_access_token_agents (
    token_id uuid NOT NULL,
    agent_id uuid NOT NULL
);


--
-- Name: chat_access_tokens; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.chat_access_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    token_digest bytea NOT NULL,
    token_prefix character varying(32) NOT NULL,
    label character varying(255) NOT NULL,
    created_by_operator_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    last_used_at timestamp with time zone,
    CONSTRAINT ck_chat_access_tokens_expiry_after_creation CHECK ((expires_at > created_at)),
    CONSTRAINT ck_chat_access_tokens_label_valid CHECK (((length(label) >= 1) AND (length(label) <= 255) AND ((label)::text = btrim((label)::text)))),
    CONSTRAINT ck_chat_access_tokens_last_use_after_creation CHECK (((last_used_at IS NULL) OR (last_used_at >= created_at))),
    CONSTRAINT ck_chat_access_tokens_revocation_after_creation CHECK (((revoked_at IS NULL) OR (revoked_at >= created_at))),
    CONSTRAINT ck_chat_access_tokens_token_digest_length CHECK ((octet_length(token_digest) = 32)),
    CONSTRAINT ck_chat_access_tokens_token_prefix_valid CHECK (((length(token_prefix) >= 8) AND (length(token_prefix) <= 32) AND ((token_prefix)::text = btrim((token_prefix)::text))))
);


--
-- Name: channel_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_bindings (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    connection_id uuid NOT NULL,
    server_id character varying(20) NOT NULL,
    listen_channel_id character varying(20) NOT NULL,
    agent_id uuid NOT NULL,
    reply_policy character varying(24) NOT NULL,
    reply_channel_id character varying(20),
    allowed_role_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    allowed_user_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    rate_requests integer DEFAULT 5 NOT NULL,
    rate_window_seconds integer DEFAULT 60 NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    health character varying(16) DEFAULT 'DRAFT'::character varying NOT NULL,
    sanitized_error text,
    validated_at timestamp with time zone,
    version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    deleted_at timestamp with time zone,
    CONSTRAINT ck_channel_bindings_allowlists_array CHECK (((jsonb_typeof(allowed_role_ids) = 'array'::text) AND (jsonb_typeof(allowed_user_ids) = 'array'::text))),
    CONSTRAINT ck_channel_bindings_deleted_binding_disabled CHECK (((deleted_at IS NULL) OR (NOT enabled))),
    CONSTRAINT ck_channel_bindings_enabled_state_valid CHECK (((NOT enabled) OR (((health)::text = 'HEALTHY'::text) AND (validated_at IS NOT NULL)))),
    CONSTRAINT ck_channel_bindings_health_valid CHECK (((health)::text = ANY ((ARRAY['DRAFT'::character varying, 'HEALTHY'::character varying, 'UNHEALTHY'::character varying])::text[]))),
    CONSTRAINT ck_channel_bindings_rate_policy_valid CHECK ((((rate_requests >= 1) AND (rate_requests <= 100)) AND ((rate_window_seconds >= 1) AND (rate_window_seconds <= 86400)))),
    CONSTRAINT ck_channel_bindings_reply_channel_valid CHECK ((((reply_policy)::text = 'SELECTED_CHANNEL'::text) = (reply_channel_id IS NOT NULL))),
    CONSTRAINT ck_channel_bindings_reply_policy_valid CHECK (((reply_policy)::text = ANY ((ARRAY['SAME_CHANNEL'::character varying, 'THREAD'::character varying, 'SELECTED_CHANNEL'::character varying])::text[]))),
    CONSTRAINT ck_channel_bindings_sanitized_error_bounded CHECK (((sanitized_error IS NULL) OR (length(sanitized_error) <= 1000))),
    CONSTRAINT ck_channel_bindings_version_positive CHECK ((version > 0))
);


--
-- Name: channel_binding_triggers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.channel_binding_triggers (
    binding_id uuid NOT NULL,
    connection_id uuid NOT NULL,
    server_id character varying(20) NOT NULL,
    listen_channel_id character varying(20) NOT NULL,
    enabled boolean NOT NULL,
    trigger_type character varying(24) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_channel_binding_triggers_type_valid CHECK (((trigger_type)::text = ANY ((ARRAY['MENTION'::character varying, 'SLASH_COMMAND'::character varying])::text[])))
);


--
-- Name: claims; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.claims (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    wiki_version_id uuid NOT NULL,
    wiki_page_id uuid NOT NULL,
    stable_id character varying(128) NOT NULL,
    statement text NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, statement)) STORED
);


--
-- Name: discord_conversations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_conversations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    binding_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    agent_version_id uuid NOT NULL,
    external_user_id character varying(20) NOT NULL,
    destination_id character varying(20) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_activity_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT ck_discord_conversations_destination_valid CHECK (((destination_id)::text ~ '^[1-9][0-9]{0,19}$'::text)),
    CONSTRAINT ck_discord_conversations_expiry_valid CHECK (((expires_at > last_activity_at) AND (updated_at >= created_at) AND (last_activity_at >= created_at))),
    CONSTRAINT ck_discord_conversations_user_valid CHECK (((external_user_id)::text ~ '^[1-9][0-9]{0,19}$'::text))
);


--
-- Name: discord_conversation_messages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_conversation_messages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    conversation_id uuid NOT NULL,
    sequence integer NOT NULL,
    role character varying(16) NOT NULL,
    markdown text NOT NULL,
    estimated_tokens integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_discord_conversation_messages_markdown_valid CHECK (((octet_length(markdown) >= 1) AND (octet_length(markdown) <= 32768))),
    CONSTRAINT ck_discord_conversation_messages_role_valid CHECK (((role)::text = ANY ((ARRAY['USER'::character varying, 'ASSISTANT'::character varying])::text[]))),
    CONSTRAINT ck_discord_conversation_messages_sequence_positive CHECK ((sequence > 0)),
    CONSTRAINT ck_discord_conversation_messages_tokens_valid CHECK (((estimated_tokens >= 1) AND (estimated_tokens <= 32768)))
);


--
-- Name: credential_rotation_attempts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_rotation_attempts (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    credential_id uuid NOT NULL,
    old_secret_version integer NOT NULL,
    new_secret_version integer NOT NULL,
    new_key_id character varying(128) NOT NULL,
    status character varying(16) DEFAULT 'PENDING'::character varying NOT NULL,
    sanitized_error text,
    actor_operator_id uuid NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    CONSTRAINT ck_credential_rotation_attempts_finished_state_valid CHECK (((((status)::text = 'PENDING'::text) AND (finished_at IS NULL)) OR (((status)::text = ANY ((ARRAY['SUCCEEDED'::character varying, 'FAILED'::character varying])::text[])) AND (finished_at IS NOT NULL)))),
    CONSTRAINT ck_credential_rotation_attempts_secret_versions_advance CHECK (((old_secret_version > 0) AND (new_secret_version > old_secret_version))),
    CONSTRAINT ck_credential_rotation_attempts_status_valid CHECK (((status)::text = ANY ((ARRAY['PENDING'::character varying, 'SUCCEEDED'::character varying, 'FAILED'::character varying])::text[])))
);


--
-- Name: credentials; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credentials (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    kind character varying(32) NOT NULL,
    label character varying(255) NOT NULL,
    masked_value character varying(64) NOT NULL,
    key_id character varying(128) NOT NULL,
    nonce bytea NOT NULL,
    ciphertext bytea NOT NULL,
    secret_version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    rotated_at timestamp with time zone,
    deleted_at timestamp with time zone,
    CONSTRAINT ck_credentials_kind_valid CHECK (((kind)::text = ANY ((ARRAY['REPOSITORY_HTTPS'::character varying, 'WEBSITE_HEADER'::character varying, 'PROVIDER_API_KEY'::character varying, 'DISCORD_BOT_TOKEN'::character varying, 'TINYFISH_API_KEY'::character varying])::text[]))),
    CONSTRAINT ck_credentials_nonce_length CHECK ((octet_length(nonce) = 12)),
    CONSTRAINT ck_credentials_secret_version_positive CHECK ((secret_version > 0))
);


--
-- Name: discord_channels; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_channels (
    connection_id uuid NOT NULL,
    server_id character varying(20) NOT NULL,
    channel_id character varying(20) NOT NULL,
    parent_id character varying(20),
    name character varying(255) NOT NULL,
    channel_type integer NOT NULL,
    "position" integer NOT NULL,
    effective_bot_permissions bigint NOT NULL,
    everyone_can_view boolean NOT NULL,
    refreshed_at timestamp with time zone NOT NULL,
    viewer_role_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    viewer_user_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    audience_overwrite_sha256 bytea DEFAULT decode('4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945'::text, 'hex'::text) NOT NULL,
    CONSTRAINT ck_discord_channels_channel_metadata_nonnegative CHECK ((("position" >= 0) AND (effective_bot_permissions >= 0))),
    CONSTRAINT ck_discord_channels_channel_type_supported CHECK ((channel_type = ANY (ARRAY[0, 11]))),
    CONSTRAINT ck_discord_channels_audience_overwrite_sha256_length CHECK ((octet_length(audience_overwrite_sha256) = 32)),
    CONSTRAINT ck_discord_channels_viewer_principals_array CHECK (((jsonb_typeof(viewer_role_ids) = 'array'::text) AND (jsonb_typeof(viewer_user_ids) = 'array'::text)))
);


--
-- Name: discord_connections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_connections (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    display_name character varying(255) NOT NULL,
    display_key character varying(255) NOT NULL,
    credential_id uuid NOT NULL,
    credential_version integer NOT NULL,
    application_id character varying(20),
    bot_user_id character varying(20),
    bot_username character varying(255),
    avatar_hash character varying(255),
    lifecycle character varying(16) DEFAULT 'ENABLED'::character varying NOT NULL,
    state character varying(16) DEFAULT 'CONNECTING'::character varying NOT NULL,
    gateway_latency_ms integer,
    last_heartbeat_at timestamp with time zone,
    last_event_at timestamp with time zone,
    sanitized_error text,
    version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_discord_connections_gateway_latency_nonnegative CHECK (((gateway_latency_ms IS NULL) OR (gateway_latency_ms >= 0))),
    CONSTRAINT ck_discord_connections_identity_pair_valid CHECK (((application_id IS NULL) = (bot_user_id IS NULL))),
    CONSTRAINT ck_discord_connections_lifecycle_valid CHECK (((lifecycle)::text = ANY ((ARRAY['ENABLED'::character varying, 'DISABLED'::character varying])::text[]))),
    CONSTRAINT ck_discord_connections_sanitized_error_bounded CHECK (((sanitized_error IS NULL) OR (length(sanitized_error) <= 1000))),
    CONSTRAINT ck_discord_connections_state_valid CHECK (((state)::text = ANY ((ARRAY['DISABLED'::character varying, 'CONNECTING'::character varying, 'READY'::character varying, 'DEGRADED'::character varying])::text[]))),
    CONSTRAINT ck_discord_connections_versions_positive CHECK (((credential_version > 0) AND (version > 0)))
);


--
-- Name: discord_roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_roles (
    connection_id uuid NOT NULL,
    server_id character varying(20) NOT NULL,
    role_id character varying(20) NOT NULL,
    name character varying(255) NOT NULL,
    "position" integer NOT NULL,
    refreshed_at timestamp with time zone NOT NULL,
    CONSTRAINT ck_discord_roles_position_nonnegative CHECK (("position" >= 0))
);


--
-- Name: discord_servers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discord_servers (
    connection_id uuid NOT NULL,
    server_id character varying(20) NOT NULL,
    name character varying(255) NOT NULL,
    icon_hash character varying(255),
    owner boolean DEFAULT false NOT NULL,
    refreshed_at timestamp with time zone NOT NULL
);


--
-- Name: discovery_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.discovery_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    endpoint_id uuid NOT NULL,
    job_id uuid NOT NULL,
    captured_configuration_version integer NOT NULL,
    captured_credential_version integer,
    tls_required boolean NOT NULL,
    requested_by_operator_id uuid NOT NULL,
    status character varying(16) DEFAULT 'PENDING'::character varying NOT NULL,
    model_ids jsonb DEFAULT '[]'::jsonb NOT NULL,
    raw_response jsonb,
    tls_verified boolean,
    authentication_succeeded boolean,
    http_status integer,
    response_sha256 bytea,
    model_count integer,
    sanitized_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    CONSTRAINT ck_discovery_runs_configuration_version_positive CHECK ((captured_configuration_version > 0)),
    CONSTRAINT ck_discovery_runs_credential_version_positive CHECK (((captured_credential_version IS NULL) OR (captured_credential_version > 0))),
    CONSTRAINT ck_discovery_runs_http_status_valid CHECK (((http_status IS NULL) OR ((http_status >= 100) AND (http_status <= 599)))),
    CONSTRAINT ck_discovery_runs_model_count_nonnegative CHECK (((model_count IS NULL) OR (model_count >= 0))),
    CONSTRAINT ck_discovery_runs_model_ids_array CHECK ((jsonb_typeof(model_ids) = 'array'::text)),
    CONSTRAINT ck_discovery_runs_raw_response_bounded CHECK (((raw_response IS NULL) OR (octet_length((raw_response)::text) <= 1048576))),
    CONSTRAINT ck_discovery_runs_raw_response_object CHECK (((raw_response IS NULL) OR (jsonb_typeof(raw_response) = 'object'::text))),
    CONSTRAINT ck_discovery_runs_response_sha256_length CHECK (((response_sha256 IS NULL) OR (octet_length(response_sha256) = 32))),
    CONSTRAINT ck_discovery_runs_result_state_valid CHECK (((((status)::text = 'PENDING'::text) AND (started_at IS NULL) AND (completed_at IS NULL) AND (raw_response IS NULL) AND (response_sha256 IS NULL) AND (model_count IS NULL) AND (sanitized_error IS NULL)) OR (((status)::text = 'RUNNING'::text) AND (started_at IS NOT NULL) AND (completed_at IS NULL) AND (raw_response IS NULL) AND (response_sha256 IS NULL) AND (model_count IS NULL) AND (sanitized_error IS NULL)) OR (((status)::text = 'SUCCEEDED'::text) AND (started_at IS NOT NULL) AND (completed_at IS NOT NULL) AND (raw_response IS NOT NULL) AND (response_sha256 IS NOT NULL) AND (model_count IS NOT NULL) AND ((NOT tls_required) OR tls_verified) AND authentication_succeeded AND ((http_status >= 200) AND (http_status <= 299)) AND (sanitized_error IS NULL)) OR (((status)::text = 'FAILED'::text) AND (started_at IS NOT NULL) AND (completed_at IS NOT NULL) AND (raw_response IS NULL) AND (response_sha256 IS NULL) AND (model_count IS NULL) AND (sanitized_error IS NOT NULL)) OR (((status)::text = 'SUPERSEDED'::text) AND (started_at IS NOT NULL) AND (completed_at IS NOT NULL) AND (((raw_response IS NOT NULL) AND (response_sha256 IS NOT NULL) AND (model_count IS NOT NULL) AND (sanitized_error IS NULL)) OR ((raw_response IS NULL) AND (response_sha256 IS NULL) AND (model_count IS NULL) AND (sanitized_error IS NOT NULL)))))),
    CONSTRAINT ck_discovery_runs_status_valid CHECK (((status)::text = ANY ((ARRAY['PENDING'::character varying, 'RUNNING'::character varying, 'SUCCEEDED'::character varying, 'FAILED'::character varying, 'SUPERSEDED'::character varying])::text[]))),
    CONSTRAINT ck_discovery_runs_timestamps_ordered CHECK (((started_at IS NULL) OR ((started_at >= created_at) AND ((completed_at IS NULL) OR (completed_at >= started_at)))))
);


--
-- Name: documentation_pages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.documentation_pages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    run_id uuid NOT NULL,
    job_id uuid NOT NULL,
    "position" integer NOT NULL,
    slug character varying(255) NOT NULL,
    title character varying(255) NOT NULL,
    purpose text NOT NULL,
    related_pages jsonb DEFAULT '[]'::jsonb NOT NULL,
    source_seed_paths jsonb DEFAULT '[]'::jsonb NOT NULL,
    status character varying(16) DEFAULT 'PENDING'::character varying NOT NULL,
    submission_digest bytea,
    content_sha256 bytea,
    claims_sha256 bytea,
    sanitized_error text,
    attempt_count integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    model_calls integer DEFAULT 0 NOT NULL,
    input_tokens integer DEFAULT 0 NOT NULL,
    output_tokens integer DEFAULT 0 NOT NULL,
    total_tokens integer DEFAULT 0 NOT NULL,
    truncated_tool_results integer DEFAULT 0 NOT NULL,
    CONSTRAINT ck_documentation_pages_claims_sha256_length CHECK (((claims_sha256 IS NULL) OR (octet_length(claims_sha256) = 32))),
    CONSTRAINT ck_documentation_pages_content_sha256_length CHECK (((content_sha256 IS NULL) OR (octet_length(content_sha256) = 32))),
    CONSTRAINT ck_documentation_pages_counters_nonnegative CHECK ((("position" >= 0) AND (attempt_count >= 0))),
    CONSTRAINT ck_documentation_pages_related_pages_array CHECK ((jsonb_typeof(related_pages) = 'array'::text)),
    CONSTRAINT ck_documentation_pages_result_state_valid CHECK (((((status)::text = ANY ((ARRAY['PENDING'::character varying, 'RUNNING'::character varying])::text[])) AND (completed_at IS NULL) AND (submission_digest IS NULL) AND (content_sha256 IS NULL) AND (claims_sha256 IS NULL)) OR (((status)::text = 'COMPLETE'::text) AND (completed_at IS NOT NULL) AND (submission_digest IS NOT NULL) AND (content_sha256 IS NOT NULL) AND (claims_sha256 IS NOT NULL) AND (sanitized_error IS NULL)) OR (((status)::text = 'SKIPPED'::text) AND (completed_at IS NOT NULL) AND (submission_digest IS NULL) AND (content_sha256 IS NULL) AND (claims_sha256 IS NULL)))),
    CONSTRAINT ck_documentation_pages_sanitized_error_bounded CHECK (((sanitized_error IS NULL) OR (length(sanitized_error) <= 1000))),
    CONSTRAINT ck_documentation_pages_source_seed_paths_array CHECK ((jsonb_typeof(source_seed_paths) = 'array'::text)),
    CONSTRAINT ck_documentation_pages_status_valid CHECK (((status)::text = ANY ((ARRAY['PENDING'::character varying, 'RUNNING'::character varying, 'COMPLETE'::character varying, 'SKIPPED'::character varying])::text[]))),
    CONSTRAINT ck_documentation_pages_submission_digest_length CHECK (((submission_digest IS NULL) OR (octet_length(submission_digest) = 32))),
    CONSTRAINT ck_documentation_pages_usage_nonnegative CHECK (((model_calls >= 0) AND (input_tokens >= 0) AND (output_tokens >= 0) AND (total_tokens >= 0) AND (truncated_tool_results >= 0)))
);


--
-- Name: documentation_run_models; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.documentation_run_models (
    run_id uuid NOT NULL,
    role character varying(32) NOT NULL,
    model_profile_id uuid NOT NULL,
    model_profile_version_id uuid NOT NULL,
    profile_version integer NOT NULL,
    provider_endpoint_id uuid NOT NULL,
    captured_endpoint_configuration_version integer CONSTRAINT documentation_run_models_captured_endpoint_configurati_not_null NOT NULL,
    captured_credential_version integer,
    reasoning_effort character varying(16) DEFAULT 'NONE'::character varying NOT NULL,
    max_concurrent_tasks integer NOT NULL,
    CONSTRAINT ck_documentation_run_models_credential_version_positive CHECK (((captured_credential_version IS NULL) OR (captured_credential_version > 0))),
    CONSTRAINT ck_documentation_run_models_endpoint_config_version_positive CHECK ((captured_endpoint_configuration_version > 0)),
    CONSTRAINT ck_documentation_run_models_profile_version_positive CHECK ((profile_version > 0)),
    CONSTRAINT ck_documentation_run_models_max_concurrent_tasks_valid CHECK (((max_concurrent_tasks >= 1) AND (max_concurrent_tasks <= 32))),
    CONSTRAINT ck_documentation_run_models_reasoning_effort_valid CHECK (((reasoning_effort)::text = ANY ((ARRAY['NONE'::character varying, 'MINIMAL'::character varying, 'LOW'::character varying, 'MEDIUM'::character varying, 'HIGH'::character varying, 'MAX'::character varying])::text[]))),
    CONSTRAINT ck_documentation_run_models_role_valid CHECK (((role)::text = ANY ((ARRAY['DOCUMENTATION_PLANNER'::character varying, 'DOCUMENTATION_WRITER'::character varying])::text[])))
);


--
-- Name: documentation_run_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.documentation_run_sources (
    configuration_version integer NOT NULL CHECK (configuration_version > 0),
    run_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_revision_id uuid NOT NULL,
    fingerprint bytea NOT NULL,
    native_version character varying(128) NOT NULL,
    CONSTRAINT ck_documentation_run_sources_fingerprint_length CHECK ((octet_length(fingerprint) = 32))
);


--
-- Name: documentation_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.documentation_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    knowledge_base_id uuid NOT NULL,
    status character varying(24) DEFAULT 'PREPARING'::character varying NOT NULL,
    prepare_job_id uuid NOT NULL,
    knowledge_base_version integer NOT NULL,
    instructions text NOT NULL,
    language character varying(35) NOT NULL,
    prior_wiki_version_id uuid,
    plan_digest bytea,
    published_wiki_version_id uuid,
    sanitized_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    planner_model_calls integer DEFAULT 0 NOT NULL,
    planner_input_tokens integer DEFAULT 0 NOT NULL,
    planner_output_tokens integer DEFAULT 0 NOT NULL,
    planner_total_tokens integer DEFAULT 0 NOT NULL,
    planner_truncated_tool_results integer DEFAULT 0 NOT NULL,
    CONSTRAINT ck_documentation_runs_completion_state_valid CHECK ((((status)::text = ANY ((ARRAY['NO_OP'::character varying, 'PUBLISHED'::character varying, 'INTERRUPTED'::character varying, 'FAILED'::character varying])::text[])) = (completed_at IS NOT NULL))),
    CONSTRAINT ck_documentation_runs_knowledge_base_version_positive CHECK ((knowledge_base_version > 0)),
    CONSTRAINT ck_documentation_runs_plan_digest_length CHECK (((plan_digest IS NULL) OR (octet_length(plan_digest) = 32))),
    CONSTRAINT ck_documentation_runs_planner_usage_nonnegative CHECK (((planner_model_calls >= 0) AND (planner_input_tokens >= 0) AND (planner_output_tokens >= 0) AND (planner_total_tokens >= 0) AND (planner_truncated_tool_results >= 0))),
    CONSTRAINT ck_documentation_runs_sanitized_error_bounded CHECK (((sanitized_error IS NULL) OR (length(sanitized_error) <= 1000))),
    CONSTRAINT ck_documentation_runs_status_valid CHECK (((status)::text = ANY ((ARRAY['PREPARING'::character varying, 'PLANNING'::character varying, 'GENERATING'::character varying, 'FINALIZING'::character varying, 'NO_OP'::character varying, 'PUBLISHED'::character varying, 'INTERRUPTED'::character varying, 'FAILED'::character varying])::text[])))
);


--
-- Name: event_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.event_log (
    sequence bigint NOT NULL,
    event_type character varying(128) NOT NULL,
    resource_type character varying(64) NOT NULL,
    resource_id uuid NOT NULL,
    snapshot jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_event_log_snapshot_object CHECK ((jsonb_typeof(snapshot) = 'object'::text))
);


--
-- Name: event_log_sequence_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.event_log ALTER COLUMN sequence ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.event_log_sequence_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: event_stream_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.event_stream_state (
    id smallint DEFAULT 1 NOT NULL,
    pruned_through bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_event_stream_state_pruned_through_nonnegative CHECK ((pruned_through >= 0)),
    CONSTRAINT ck_event_stream_state_singleton CHECK ((id = 1))
);


INSERT INTO public.event_stream_state (id, pruned_through) VALUES (1, 0);


--
-- Name: evidence; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.evidence (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    claim_id uuid NOT NULL,
    evidence_id character varying(128) NOT NULL,
    source_id uuid NOT NULL,
    source_revision_id uuid NOT NULL,
    source_fingerprint bytea NOT NULL,
    native_version character varying(128) NOT NULL,
    path character varying(4096) NOT NULL,
    start_line integer,
    end_line integer,
    resource character varying(8192) NOT NULL,
    CONSTRAINT ck_evidence_line_range_valid CHECK ((((start_line IS NULL) AND (end_line IS NULL)) OR ((start_line > 0) AND (end_line >= start_line) AND (end_line <= 1000000)))),
    CONSTRAINT ck_evidence_source_fingerprint_length CHECK ((octet_length(source_fingerprint) = 32))
);


--
-- Name: idempotency_records; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.idempotency_records (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    scope character varying(255) NOT NULL,
    request_key character varying(255) NOT NULL,
    operation character varying(128) NOT NULL,
    request_digest bytea NOT NULL,
    result_type character varying(64) NOT NULL,
    result_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT ck_idempotency_records_expiry_after_creation CHECK ((expires_at > created_at)),
    CONSTRAINT ck_idempotency_records_request_digest_length CHECK ((octet_length(request_digest) = 32))
);


--
-- Name: job_attempts; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.job_attempts (
    job_id uuid NOT NULL,
    attempt_number integer NOT NULL,
    lease_generation bigint NOT NULL,
    worker_id character varying(255) NOT NULL,
    heartbeat_at timestamp with time zone NOT NULL,
    outcome character varying(32),
    sanitized_error text,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    CONSTRAINT ck_job_attempts_attempt_number_positive CHECK ((attempt_number > 0)),
    CONSTRAINT ck_job_attempts_lease_generation_positive CHECK ((lease_generation > 0))
);


--
-- Name: job_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.job_events (
    sequence bigint NOT NULL,
    job_id uuid NOT NULL,
    attempt_number integer,
    event_kind character varying(64) NOT NULL,
    status character varying(24) NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_job_events_attempt_number_positive CHECK (((attempt_number IS NULL) OR (attempt_number > 0))),
    CONSTRAINT ck_job_events_payload_object CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT ck_job_events_status_valid CHECK (((status)::text = ANY ((ARRAY['PENDING'::character varying, 'LEASED'::character varying, 'SUCCEEDED'::character varying, 'RETRY_WAIT'::character varying, 'FAILED'::character varying, 'CANCEL_REQUESTED'::character varying, 'CANCELLED'::character varying])::text[])))
);


--
-- Name: job_events_sequence_seq; Type: SEQUENCE; Schema: public; Owner: -
--

ALTER TABLE public.job_events ALTER COLUMN sequence ADD GENERATED BY DEFAULT AS IDENTITY (
    SEQUENCE NAME public.job_events_sequence_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);


--
-- Name: jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    job_type character varying(32) NOT NULL,
    target_type character varying(64) NOT NULL,
    target_id uuid NOT NULL,
    payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    operation_key character varying(512) NOT NULL,
    concurrency_key character varying(512) DEFAULT ''::character varying NOT NULL,
    concurrency_limit integer DEFAULT 0 NOT NULL,
    status character varying(24) DEFAULT 'PENDING'::character varying NOT NULL,
    attempt_count integer DEFAULT 0 NOT NULL,
    max_attempts integer DEFAULT 3 NOT NULL,
    progress integer DEFAULT 0 NOT NULL,
    lease_owner character varying(255),
    lease_expires_at timestamp with time zone,
    lease_generation bigint DEFAULT 0 NOT NULL,
    not_before timestamp with time zone,
    result jsonb,
    sanitized_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    CONSTRAINT ck_jobs_attempts_valid CHECK (((attempt_count >= 0) AND (max_attempts > 0) AND (attempt_count <= max_attempts))),
    CONSTRAINT ck_jobs_concurrency_valid CHECK (((concurrency_key = ''::text AND concurrency_limit = 0) OR (concurrency_key <> ''::text AND concurrency_limit >= 1 AND concurrency_limit <= 32))),
    CONSTRAINT ck_jobs_job_type_valid CHECK (((job_type)::text = ANY ((ARRAY['VALIDATE_SOURCE'::character varying, 'SYNC_SOURCE'::character varying, 'PREPARE_RUN'::character varying, 'PLAN_RUN'::character varying, 'GENERATE_PAGE'::character varying, 'FINALIZE_RUN'::character varying, 'DISCOVER_ENDPOINT'::character varying, 'PROBE_MODEL'::character varying, 'REFRESH_DISCORD'::character varying, 'PURGE_KNOWLEDGE_BASE'::character varying, 'APPLY_RETENTION'::character varying])::text[]))),
    CONSTRAINT ck_jobs_lease_generation_nonnegative CHECK ((lease_generation >= 0)),
    CONSTRAINT ck_jobs_lease_state_valid CHECK (((((status)::text = ANY ((ARRAY['LEASED'::character varying, 'CANCEL_REQUESTED'::character varying])::text[])) AND (lease_owner IS NOT NULL) AND (lease_expires_at IS NOT NULL)) OR (((status)::text <> ALL ((ARRAY['LEASED'::character varying, 'CANCEL_REQUESTED'::character varying])::text[])) AND (lease_owner IS NULL) AND (lease_expires_at IS NULL)))),
    CONSTRAINT ck_jobs_payload_object CHECK ((jsonb_typeof(payload) = 'object'::text)),
    CONSTRAINT ck_jobs_progress_valid CHECK (((progress >= 0) AND (progress <= 100))),
    CONSTRAINT ck_jobs_result_object CHECK (((result IS NULL) OR (jsonb_typeof(result) = 'object'::text))),
    CONSTRAINT ck_jobs_retry_wait_not_before_present CHECK ((((status)::text <> 'RETRY_WAIT'::text) OR (not_before IS NOT NULL))),
    CONSTRAINT ck_jobs_status_valid CHECK (((status)::text = ANY ((ARRAY['PENDING'::character varying, 'LEASED'::character varying, 'SUCCEEDED'::character varying, 'RETRY_WAIT'::character varying, 'FAILED'::character varying, 'CANCEL_REQUESTED'::character varying, 'CANCELLED'::character varying])::text[])))
);


--
-- Name: knowledge_bases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.knowledge_bases (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name character varying(255) NOT NULL,
    name_key character varying(255) NOT NULL,
    access_policy character varying(16) DEFAULT 'RESTRICTED'::character varying NOT NULL,
    lifecycle character varying(24) DEFAULT 'ACTIVE'::character varying NOT NULL,
    instructions text DEFAULT ''::text NOT NULL,
    language character varying(35) DEFAULT 'en'::character varying NOT NULL,
    published_wiki_id uuid,
    archived_at timestamp with time zone,
    delete_requested_at timestamp with time zone,
    purge_after timestamp with time zone,
    deleted_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    CONSTRAINT ck_knowledge_bases_access_policy_valid CHECK (((access_policy)::text = ANY ((ARRAY['PUBLIC'::character varying, 'RESTRICTED'::character varying])::text[]))),
    CONSTRAINT ck_knowledge_bases_deleted_time_present CHECK ((((lifecycle)::text <> 'DELETED'::text) OR (deleted_at IS NOT NULL))),
    CONSTRAINT ck_knowledge_bases_lifecycle_valid CHECK (((lifecycle)::text = ANY ((ARRAY['ACTIVE'::character varying, 'ARCHIVED'::character varying, 'PENDING_DELETE'::character varying, 'DELETED'::character varying])::text[]))),
    CONSTRAINT ck_knowledge_bases_pending_delete_times_valid CHECK ((((lifecycle)::text <> 'PENDING_DELETE'::text) OR ((delete_requested_at IS NOT NULL) AND (purge_after IS NOT NULL) AND (purge_after > delete_requested_at)))),
    CONSTRAINT ck_knowledge_bases_version_positive CHECK ((version > 0))
);


--
-- Name: model_assignments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_assignments (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    knowledge_base_id uuid NOT NULL,
    role character varying(32) NOT NULL,
    model_profile_id uuid NOT NULL,
    reasoning_effort character varying(16) NOT NULL,
    answer_mode character varying(16) NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_model_assignments_answer_mode_valid CHECK (((answer_mode)::text = ANY ((ARRAY['TOOL_CALLING'::character varying, 'SINGLE_PASS'::character varying])::text[]))),
    CONSTRAINT ck_model_assignments_reasoning_effort_valid CHECK (((reasoning_effort)::text = ANY ((ARRAY['NONE'::character varying, 'MINIMAL'::character varying, 'LOW'::character varying, 'MEDIUM'::character varying, 'HIGH'::character varying, 'MAX'::character varying])::text[]))),
    CONSTRAINT ck_model_assignments_role_valid CHECK (((role)::text = ANY ((ARRAY['DOCUMENTATION_PLANNER'::character varying, 'DOCUMENTATION_WRITER'::character varying])::text[]))),
    CONSTRAINT ck_model_assignments_version_positive CHECK ((version > 0))
);


--
-- Name: model_profile_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_profile_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    profile_id uuid NOT NULL,
    version_number integer NOT NULL,
    configuration_version integer NOT NULL,
    transport character varying(32) NOT NULL,
    context_window_tokens integer,
    max_output_tokens integer,
    supports_streaming boolean,
    supports_tools boolean,
    supports_structured_output boolean,
    supports_temperature boolean,
    reasoning_transport character varying(32) NOT NULL,
    reasoning_mapping jsonb,
    timeout_seconds integer NOT NULL,
    max_retries integer NOT NULL,
    max_concurrent_tasks integer DEFAULT 1 NOT NULL,
    extra_body jsonb DEFAULT '{}'::jsonb NOT NULL,
    metadata_origin jsonb NOT NULL,
    source character varying(16) NOT NULL,
    created_by_operator_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_model_profile_versions_configuration_version_positive CHECK ((configuration_version > 0)),
    CONSTRAINT ck_model_profile_versions_context_window_tokens_positive CHECK (((context_window_tokens IS NULL) OR (context_window_tokens > 0))),
    CONSTRAINT ck_model_profile_versions_extra_body_object CHECK ((jsonb_typeof(extra_body) = 'object'::text)),
    CONSTRAINT ck_model_profile_versions_max_output_tokens_positive CHECK (((max_output_tokens IS NULL) OR (max_output_tokens > 0))),
    CONSTRAINT ck_model_profile_versions_max_concurrent_tasks_valid CHECK (((max_concurrent_tasks >= 1) AND (max_concurrent_tasks <= 32))),
    CONSTRAINT ck_model_profile_versions_metadata_origin_object CHECK ((jsonb_typeof(metadata_origin) = 'object'::text)),
    CONSTRAINT ck_model_profile_versions_reasoning_mapping_object CHECK (((reasoning_mapping IS NULL) OR (jsonb_typeof(reasoning_mapping) = 'object'::text))),
    CONSTRAINT ck_model_profile_versions_reasoning_mapping_valid CHECK ((((reasoning_transport)::text = 'CUSTOM'::text) = (reasoning_mapping IS NOT NULL))),
    CONSTRAINT ck_model_profile_versions_reasoning_transport_valid CHECK (((reasoning_transport)::text = ANY ((ARRAY['NONE'::character varying, 'REASONING_EFFORT'::character varying, 'CUSTOM'::character varying])::text[]))),
    CONSTRAINT ck_model_profile_versions_retries_valid CHECK (((max_retries >= 0) AND (max_retries <= 10))),
    CONSTRAINT ck_model_profile_versions_source_valid CHECK (((source)::text = ANY ((ARRAY['DISCOVERY'::character varying, 'OPERATOR'::character varying, 'PROBE'::character varying])::text[]))),
    CONSTRAINT ck_model_profile_versions_timeout_valid CHECK (((timeout_seconds >= 1) AND (timeout_seconds <= 60))),
    CONSTRAINT ck_model_profile_versions_transport_valid CHECK (((transport)::text = ANY ((ARRAY['CHAT_COMPLETIONS'::character varying, 'RESPONSES'::character varying])::text[]))),
    CONSTRAINT ck_model_profile_versions_version_number_positive CHECK ((version_number > 0))
);


--
-- Name: model_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_profiles (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    endpoint_id uuid NOT NULL,
    model_id character varying(512) NOT NULL,
    availability character varying(16) DEFAULT 'AVAILABLE'::character varying NOT NULL,
    current_version_id uuid NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_model_profiles_availability_valid CHECK (((availability)::text = ANY ((ARRAY['AVAILABLE'::character varying, 'UNAVAILABLE'::character varying, 'MANUAL'::character varying])::text[]))),
    CONSTRAINT ck_model_profiles_version_positive CHECK ((version > 0))
);


--
-- Name: operator_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.operator_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    operator_id uuid NOT NULL,
    token_digest bytea NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT ck_operator_sessions_expiry_after_creation CHECK ((expires_at > created_at)),
    CONSTRAINT ck_operator_sessions_token_digest_length CHECK ((octet_length(token_digest) = 32))
);


--
-- Name: operators; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.operators (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    username character varying(255) NOT NULL,
    username_key character varying(255) NOT NULL,
    password_hash text NOT NULL,
    disabled_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    CONSTRAINT ck_operators_version_positive CHECK ((version > 0))
);


--
-- Name: probe_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.probe_runs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    model_profile_id uuid NOT NULL,
    job_id uuid NOT NULL,
    captured_configuration_version integer NOT NULL,
    captured_credential_version integer,
    captured_profile_version_id uuid NOT NULL,
    requested_by_operator_id uuid NOT NULL,
    selected_checks jsonb NOT NULL,
    acknowledge_cost boolean NOT NULL,
    status character varying(16) DEFAULT 'PENDING'::character varying NOT NULL,
    findings jsonb,
    raw_response jsonb,
    sanitized_error text,
    resulting_version_id uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    CONSTRAINT ck_probe_runs_configuration_version_positive CHECK ((captured_configuration_version > 0)),
    CONSTRAINT ck_probe_runs_cost_acknowledged CHECK (acknowledge_cost),
    CONSTRAINT ck_probe_runs_credential_version_positive CHECK (((captured_credential_version IS NULL) OR (captured_credential_version > 0))),
    CONSTRAINT ck_probe_runs_findings_object CHECK (((findings IS NULL) OR (jsonb_typeof(findings) = 'object'::text))),
    CONSTRAINT ck_probe_runs_raw_response_bounded CHECK (((raw_response IS NULL) OR (octet_length((raw_response)::text) <= 65536))),
    CONSTRAINT ck_probe_runs_raw_response_object CHECK (((raw_response IS NULL) OR (jsonb_typeof(raw_response) = 'object'::text))),
    CONSTRAINT ck_probe_runs_result_state_valid CHECK (((((status)::text = 'PENDING'::text) AND (started_at IS NULL) AND (completed_at IS NULL) AND (findings IS NULL) AND (raw_response IS NULL) AND (sanitized_error IS NULL) AND (resulting_version_id IS NULL)) OR (((status)::text = 'RUNNING'::text) AND (started_at IS NOT NULL) AND (completed_at IS NULL) AND (findings IS NULL) AND (raw_response IS NULL) AND (sanitized_error IS NULL) AND (resulting_version_id IS NULL)) OR (((status)::text = 'SUCCEEDED'::text) AND (started_at IS NOT NULL) AND (completed_at IS NOT NULL) AND (findings IS NOT NULL) AND (raw_response IS NOT NULL) AND (sanitized_error IS NULL) AND (resulting_version_id IS NOT NULL)) OR (((status)::text = 'FAILED'::text) AND (started_at IS NOT NULL) AND (completed_at IS NOT NULL) AND (findings IS NULL) AND (raw_response IS NULL) AND (sanitized_error IS NOT NULL) AND (resulting_version_id IS NULL)) OR (((status)::text = 'SUPERSEDED'::text) AND (started_at IS NOT NULL) AND (completed_at IS NOT NULL) AND (resulting_version_id IS NULL) AND (((findings IS NOT NULL) AND (raw_response IS NOT NULL) AND (sanitized_error IS NULL)) OR ((findings IS NULL) AND (raw_response IS NULL) AND (sanitized_error IS NOT NULL)))))),
    CONSTRAINT ck_probe_runs_selected_checks_array CHECK ((jsonb_typeof(selected_checks) = 'array'::text)),
    CONSTRAINT ck_probe_runs_selected_checks_count CHECK (((jsonb_array_length(selected_checks) >= 1) AND (jsonb_array_length(selected_checks) <= 4))),
    CONSTRAINT ck_probe_runs_status_valid CHECK (((status)::text = ANY ((ARRAY['PENDING'::character varying, 'RUNNING'::character varying, 'SUCCEEDED'::character varying, 'FAILED'::character varying, 'SUPERSEDED'::character varying])::text[]))),
    CONSTRAINT ck_probe_runs_timestamps_ordered CHECK (((started_at IS NULL) OR ((started_at >= created_at) AND ((completed_at IS NULL) OR (completed_at >= started_at)))))
);


--
-- Name: provider_endpoints; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_endpoints (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    display_name character varying(255) NOT NULL,
    display_key character varying(255) NOT NULL,
    base_url character varying(2048) NOT NULL,
    credential_id uuid,
    headers jsonb DEFAULT '{}'::jsonb NOT NULL,
    chat_completions_path character varying(255) DEFAULT 'chat/completions'::character varying NOT NULL,
    responses_path character varying(255) DEFAULT 'responses'::character varying,
    models_path character varying(255) DEFAULT 'models'::character varying NOT NULL,
    allow_http boolean DEFAULT false NOT NULL,
    allow_private_network boolean DEFAULT false NOT NULL,
    lifecycle character varying(16) DEFAULT 'ACTIVE'::character varying NOT NULL,
    version integer DEFAULT 1 NOT NULL,
    configuration_version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    archived_at timestamp with time zone,
    health character varying(16) DEFAULT 'UNKNOWN'::character varying NOT NULL,
    health_checked_at timestamp with time zone,
    CONSTRAINT ck_provider_endpoints_archive_time_valid CHECK ((((lifecycle)::text = 'ARCHIVED'::text) = (archived_at IS NOT NULL))),
    CONSTRAINT ck_provider_endpoints_configuration_version_positive CHECK ((configuration_version > 0)),
    CONSTRAINT ck_provider_endpoints_headers_object CHECK ((jsonb_typeof(headers) = 'object'::text)),
    CONSTRAINT ck_provider_endpoints_health_state_valid CHECK (((((health)::text = 'UNKNOWN'::text) AND (health_checked_at IS NULL)) OR (((health)::text = ANY ((ARRAY['HEALTHY'::character varying, 'UNHEALTHY'::character varying])::text[])) AND (health_checked_at IS NOT NULL)))),
    CONSTRAINT ck_provider_endpoints_health_valid CHECK (((health)::text = ANY ((ARRAY['UNKNOWN'::character varying, 'HEALTHY'::character varying, 'UNHEALTHY'::character varying])::text[]))),
    CONSTRAINT ck_provider_endpoints_http_policy_valid CHECK (((lower((base_url)::text) !~~ 'http://%'::text) OR (allow_http AND allow_private_network))),
    CONSTRAINT ck_provider_endpoints_lifecycle_valid CHECK (((lifecycle)::text = ANY ((ARRAY['ACTIVE'::character varying, 'ARCHIVED'::character varying])::text[]))),
    CONSTRAINT ck_provider_endpoints_version_positive CHECK ((version > 0))
);


--
-- Name: rate_limit_buckets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.rate_limit_buckets (
    binding_id uuid NOT NULL,
    external_user_id character varying(255) NOT NULL,
    window_started_at timestamp with time zone NOT NULL,
    request_count integer NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT ck_rate_limit_buckets_expiry_after_window CHECK ((expires_at > window_started_at)),
    CONSTRAINT ck_rate_limit_buckets_request_count_positive CHECK ((request_count > 0))
);


--
-- Name: repository_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.repository_sources (
    source_id uuid NOT NULL,
    remote_url character varying(2048) NOT NULL,
    credential_username character varying(255),
    credential_id uuid,
    ref_kind character varying(16) NOT NULL,
    ref_value character varying(512) NOT NULL,
    include_patterns jsonb DEFAULT '[]'::jsonb NOT NULL,
    exclude_patterns jsonb DEFAULT '[]'::jsonb NOT NULL,
    poll_interval_seconds integer,
    CONSTRAINT ck_repository_sources_commit_ref_valid CHECK ((((ref_kind)::text <> 'COMMIT'::text) OR ((ref_value)::text ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'::text))),
    CONSTRAINT ck_repository_sources_credential_pair_valid CHECK (((credential_username IS NULL) = (credential_id IS NULL))),
    CONSTRAINT ck_repository_sources_exclude_patterns_valid CHECK (((jsonb_typeof(exclude_patterns) = 'array'::text) AND (jsonb_array_length(exclude_patterns) <= 100) AND (octet_length((exclude_patterns)::text) <= 65536))),
    CONSTRAINT ck_repository_sources_include_patterns_valid CHECK (((jsonb_typeof(include_patterns) = 'array'::text) AND (jsonb_array_length(include_patterns) <= 100) AND (octet_length((include_patterns)::text) <= 65536))),
    CONSTRAINT ck_repository_sources_poll_interval_valid CHECK (((poll_interval_seconds IS NULL) OR ((poll_interval_seconds >= 60) AND (poll_interval_seconds <= 604800)))),
    CONSTRAINT ck_repository_sources_ref_kind_valid CHECK (((ref_kind)::text = ANY ((ARRAY['BRANCH'::character varying, 'COMMIT'::character varying])::text[]))),
    CONSTRAINT ck_repository_sources_remote_https CHECK ((lower((remote_url)::text) ~~ 'https://%'::text)),
    CONSTRAINT ck_repository_sources_remote_without_query_or_fragment CHECK (((POSITION(('?'::text) IN (remote_url)) = 0) AND (POSITION(('#'::text) IN (remote_url)) = 0)))
);


--
-- Name: source_revisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.source_revisions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    source_id uuid NOT NULL,
    observed_ref_kind character varying(16) NOT NULL,
    observed_ref character varying(2048) NOT NULL,
    native_version character varying(128) NOT NULL,
    fingerprint bytea NOT NULL,
    artifact_key character varying(2048) NOT NULL,
    file_count integer NOT NULL,
    byte_count bigint NOT NULL,
    ignored_paths jsonb DEFAULT '[]'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    artifact_purged_at timestamp with time zone,
    CONSTRAINT ck_source_revisions_counts_nonnegative CHECK (((file_count >= 0) AND (byte_count >= 0))),
    CONSTRAINT ck_source_revisions_fingerprint_length CHECK ((octet_length(fingerprint) = 32)),
    CONSTRAINT ck_source_revisions_ignored_paths_valid CHECK (((jsonb_typeof(ignored_paths) = 'array'::text) AND (jsonb_array_length(ignored_paths) <= 1000) AND (octet_length((ignored_paths)::text) <= 1048576))),
    CONSTRAINT ck_source_revisions_observed_ref_kind_valid CHECK (((observed_ref_kind)::text = ANY ((ARRAY['BRANCH'::character varying, 'COMMIT'::character varying, 'ROOT'::character varying])::text[])))
);


--
-- Name: source_syncs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.source_syncs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    source_id uuid NOT NULL,
    job_id uuid NOT NULL,
    sync_kind character varying(16) NOT NULL,
    requested_by_operator_id uuid,
    captured_source_version integer NOT NULL,
    captured_configuration_version integer NOT NULL,
    captured_privacy character varying(16) NOT NULL,
    captured_remote_url character varying(2048) NOT NULL,
    captured_credential_username character varying(255),
    captured_credential_id uuid,
    captured_credential_version integer,
    captured_ref_kind character varying(16),
    captured_ref_value character varying(512),
    captured_include_patterns jsonb,
    captured_exclude_patterns jsonb,
    candidate_revision_id uuid,
    status character varying(16) DEFAULT 'PENDING'::character varying NOT NULL,
    result_revision_id uuid,
    resolved_native_version character varying(128),
    sanitized_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    captured_source_kind character varying(32) DEFAULT 'REPOSITORY'::character varying NOT NULL,
    captured_credential_header character varying(127),
    captured_credential_prefix character varying(128),
    captured_max_concurrency integer,
    captured_requests_per_second integer,
    captured_max_pages integer,
    captured_max_page_bytes integer,
    captured_max_total_bytes bigint,
    captured_max_depth integer,
    captured_previous_revision_id uuid,
    captured_acquisition_mode character varying(16) DEFAULT 'BUILTIN_CRAWL'::character varying NOT NULL,
    captured_tinyfish_credential_id uuid,
    captured_tinyfish_credential_version integer,
    CONSTRAINT ck_source_syncs_candidate_revision_valid CHECK ((((sync_kind)::text = 'SYNC'::text) = (candidate_revision_id IS NOT NULL))),
    CONSTRAINT ck_source_syncs_captured_acquisition_mode_valid CHECK (((captured_acquisition_mode)::text = ANY ((ARRAY['BUILTIN_CRAWL'::character varying, 'TINYFISH_CRAWL'::character varying, 'DIRECT_JSON_API'::character varying])::text[]))),
    CONSTRAINT ck_source_syncs_captured_commit_ref_valid CHECK (((captured_ref_kind IS NULL) OR ((captured_ref_kind)::text <> 'COMMIT'::text) OR ((captured_ref_value)::text ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'::text))),
    CONSTRAINT ck_source_syncs_captured_credential_valid CHECK ((((captured_credential_username IS NULL) AND (captured_credential_header IS NULL) AND (captured_credential_id IS NULL) AND (captured_credential_version IS NULL)) OR (((captured_source_kind)::text = 'REPOSITORY'::text) AND (captured_credential_username IS NOT NULL) AND (captured_credential_header IS NULL) AND (captured_credential_id IS NOT NULL) AND (captured_credential_version > 0)) OR (((captured_source_kind)::text = 'WEBSITE'::text) AND (captured_credential_username IS NULL) AND (captured_credential_header IS NOT NULL) AND (captured_credential_id IS NOT NULL) AND (captured_credential_version > 0)))),
    CONSTRAINT ck_source_syncs_captured_direct_json_api_limits_valid CHECK ((((captured_source_kind)::text <> 'WEBSITE'::text) OR ((captured_acquisition_mode)::text <> 'DIRECT_JSON_API'::text) OR ((captured_max_pages = 1) AND (captured_max_depth = 0)))),
    CONSTRAINT ck_source_syncs_captured_exclude_patterns_valid CHECK (((captured_exclude_patterns IS NULL) OR ((jsonb_typeof(captured_exclude_patterns) = 'array'::text) AND (jsonb_array_length(captured_exclude_patterns) <= 100) AND (octet_length((captured_exclude_patterns)::text) <= 65536)))),
    CONSTRAINT ck_source_syncs_captured_include_patterns_valid CHECK (((captured_include_patterns IS NULL) OR ((jsonb_typeof(captured_include_patterns) = 'array'::text) AND (jsonb_array_length(captured_include_patterns) <= 100) AND (octet_length((captured_include_patterns)::text) <= 65536)))),
    CONSTRAINT ck_source_syncs_captured_kind_configuration_valid CHECK (((((captured_source_kind)::text = 'REPOSITORY'::text) AND (captured_ref_kind IS NOT NULL) AND (captured_ref_value IS NOT NULL) AND (captured_include_patterns IS NOT NULL) AND (captured_exclude_patterns IS NOT NULL) AND (captured_max_concurrency IS NULL)) OR (((captured_source_kind)::text = 'WEBSITE'::text) AND (captured_ref_kind IS NULL) AND (captured_ref_value IS NULL) AND (captured_include_patterns IS NULL) AND (captured_exclude_patterns IS NULL) AND ((captured_max_concurrency >= 1) AND (captured_max_concurrency <= 16)) AND ((captured_requests_per_second >= 1) AND (captured_requests_per_second <= 100)) AND ((captured_max_pages >= 1) AND (captured_max_pages <= 10000)) AND ((captured_max_page_bytes >= 1024) AND (captured_max_page_bytes <= 10485760)) AND ((captured_max_total_bytes >= captured_max_page_bytes) AND (captured_max_total_bytes <= 1073741824)) AND ((captured_max_depth >= 0) AND (captured_max_depth <= 10))))),
    CONSTRAINT ck_source_syncs_captured_privacy_valid CHECK (((captured_privacy)::text = ANY ((ARRAY['PUBLIC'::character varying, 'PRIVATE'::character varying])::text[]))),
    CONSTRAINT ck_source_syncs_captured_ref_kind_valid CHECK (((captured_ref_kind IS NULL) OR ((captured_ref_kind)::text = ANY ((ARRAY['BRANCH'::character varying, 'COMMIT'::character varying])::text[])))),
    CONSTRAINT ck_source_syncs_captured_remote_https CHECK ((lower((captured_remote_url)::text) ~~ 'https://%'::text)),
    CONSTRAINT ck_source_syncs_captured_remote_without_query_or_fragment CHECK (((POSITION(('?'::text) IN (captured_remote_url)) = 0) AND (POSITION(('#'::text) IN (captured_remote_url)) = 0))),
    CONSTRAINT ck_source_syncs_captured_source_kind_valid CHECK (((captured_source_kind)::text = ANY ((ARRAY['REPOSITORY'::character varying, 'WEBSITE'::character varying])::text[]))),
    CONSTRAINT ck_source_syncs_captured_tinyfish_valid CHECK ((((captured_tinyfish_credential_id IS NULL) AND (captured_tinyfish_credential_version IS NULL)) OR (((captured_source_kind)::text = 'WEBSITE'::text) AND ((captured_privacy)::text = 'PUBLIC'::text) AND ((captured_acquisition_mode)::text = 'TINYFISH_CRAWL'::text) AND (captured_credential_header IS NULL) AND (captured_credential_id IS NULL) AND (captured_tinyfish_credential_id IS NOT NULL) AND (captured_tinyfish_credential_version > 0)))),
    CONSTRAINT ck_source_syncs_configuration_version_positive CHECK ((captured_configuration_version > 0)),
    CONSTRAINT ck_source_syncs_result_state_valid CHECK (((((status)::text = 'PENDING'::text) AND (started_at IS NULL) AND (completed_at IS NULL) AND (result_revision_id IS NULL) AND (resolved_native_version IS NULL) AND (sanitized_error IS NULL)) OR (((status)::text = 'RUNNING'::text) AND (started_at IS NOT NULL) AND (completed_at IS NULL) AND (result_revision_id IS NULL) AND (resolved_native_version IS NULL) AND (sanitized_error IS NULL)) OR (((status)::text = 'SUCCEEDED'::text) AND (started_at IS NOT NULL) AND (completed_at IS NOT NULL) AND (sanitized_error IS NULL) AND (resolved_native_version IS NOT NULL) AND ((((sync_kind)::text = 'VALIDATION'::text) AND (result_revision_id IS NULL)) OR (((sync_kind)::text = 'SYNC'::text) AND (result_revision_id IS NOT NULL)))) OR (((status)::text = 'FAILED'::text) AND (started_at IS NOT NULL) AND (completed_at IS NOT NULL) AND (result_revision_id IS NULL) AND (resolved_native_version IS NULL) AND (sanitized_error IS NOT NULL)) OR (((status)::text = 'SUPERSEDED'::text) AND (started_at IS NOT NULL) AND (completed_at IS NOT NULL) AND (result_revision_id IS NULL) AND (sanitized_error IS NULL)))),
    CONSTRAINT ck_source_syncs_sanitized_error_bounded CHECK (((sanitized_error IS NULL) OR (length(sanitized_error) <= 1000))),
    CONSTRAINT ck_source_syncs_source_version_positive CHECK ((captured_source_version > 0)),
    CONSTRAINT ck_source_syncs_status_valid CHECK (((status)::text = ANY ((ARRAY['PENDING'::character varying, 'RUNNING'::character varying, 'SUCCEEDED'::character varying, 'FAILED'::character varying, 'SUPERSEDED'::character varying])::text[]))),
    CONSTRAINT ck_source_syncs_sync_kind_valid CHECK (((sync_kind)::text = ANY ((ARRAY['VALIDATION'::character varying, 'SYNC'::character varying])::text[]))),
    CONSTRAINT ck_source_syncs_timestamps_ordered CHECK (((started_at IS NULL) OR ((started_at >= created_at) AND ((completed_at IS NULL) OR (completed_at >= started_at)))))
);


--
-- Name: sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sources (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    knowledge_base_id uuid NOT NULL,
    kind character varying(32) DEFAULT 'REPOSITORY'::character varying NOT NULL,
    display_name character varying(255) NOT NULL,
    display_key character varying(255) NOT NULL,
    privacy character varying(16) NOT NULL,
    lifecycle character varying(16) DEFAULT 'DRAFT'::character varying NOT NULL,
    health character varying(16) DEFAULT 'UNKNOWN'::character varying NOT NULL,
    sanitized_error text,
    checked_at timestamp with time zone,
    current_revision_id uuid,
    version integer DEFAULT 1 NOT NULL,
    configuration_version integer DEFAULT 1 NOT NULL,
    validated_configuration_version integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    disabled_at timestamp with time zone,
    removed_at timestamp with time zone,
    CONSTRAINT ck_sources_active_configuration_validated CHECK ((((lifecycle)::text <> 'ACTIVE'::text) OR (validated_configuration_version = configuration_version))),
    CONSTRAINT ck_sources_configuration_version_positive CHECK ((configuration_version > 0)),
    CONSTRAINT ck_sources_health_state_valid CHECK (((((health)::text = 'UNKNOWN'::text) AND (sanitized_error IS NULL) AND (checked_at IS NULL)) OR (((health)::text = 'HEALTHY'::text) AND (sanitized_error IS NULL) AND (checked_at IS NOT NULL)) OR (((health)::text = 'UNHEALTHY'::text) AND (sanitized_error IS NOT NULL) AND (checked_at IS NOT NULL)))),
    CONSTRAINT ck_sources_health_valid CHECK (((health)::text = ANY ((ARRAY['UNKNOWN'::character varying, 'HEALTHY'::character varying, 'UNHEALTHY'::character varying])::text[]))),
    CONSTRAINT ck_sources_kind_valid CHECK (((kind)::text = ANY ((ARRAY['REPOSITORY'::character varying, 'WEBSITE'::character varying])::text[]))),
    CONSTRAINT ck_sources_lifecycle_timestamps_valid CHECK (((((lifecycle)::text = ANY ((ARRAY['DRAFT'::character varying, 'ACTIVE'::character varying])::text[])) AND (disabled_at IS NULL) AND (removed_at IS NULL)) OR (((lifecycle)::text = 'DISABLED'::text) AND (disabled_at IS NOT NULL) AND (removed_at IS NULL)) OR (((lifecycle)::text = 'REMOVED'::text) AND (removed_at IS NOT NULL)))),
    CONSTRAINT ck_sources_lifecycle_valid CHECK (((lifecycle)::text = ANY ((ARRAY['DRAFT'::character varying, 'ACTIVE'::character varying, 'DISABLED'::character varying, 'REMOVED'::character varying])::text[]))),
    CONSTRAINT ck_sources_privacy_valid CHECK (((privacy)::text = ANY ((ARRAY['PUBLIC'::character varying, 'PRIVATE'::character varying])::text[]))),
    CONSTRAINT ck_sources_sanitized_error_bounded CHECK (((sanitized_error IS NULL) OR (length(sanitized_error) <= 1000))),
    CONSTRAINT ck_sources_validated_configuration_version_valid CHECK (((validated_configuration_version IS NULL) OR ((validated_configuration_version > 0) AND (validated_configuration_version <= configuration_version)))),
    CONSTRAINT ck_sources_version_positive CHECK ((version > 0))
);


--
-- Name: website_revision_pages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.website_revision_pages (
    source_id uuid NOT NULL,
    revision_id uuid NOT NULL,
    canonical_url character varying(4096) NOT NULL,
    content_path character varying(4096) NOT NULL,
    content_sha256 bytea NOT NULL,
    evidence_uri character varying(8192) NOT NULL,
    freshness character varying(16) NOT NULL,
    etag character varying(1024),
    last_modified character varying(255),
    reused_from_revision_id uuid,
    CONSTRAINT ck_website_revision_pages_canonical_url_https CHECK ((lower((canonical_url)::text) ~~ 'https://%'::text)),
    CONSTRAINT ck_website_revision_pages_content_sha256_length CHECK ((octet_length(content_sha256) = 32)),
    CONSTRAINT ck_website_revision_pages_freshness_valid CHECK (((freshness)::text = ANY ((ARRAY['fresh'::character varying, 'reused'::character varying])::text[]))),
    CONSTRAINT ck_website_revision_pages_reuse_state_valid CHECK ((((freshness)::text = 'reused'::text) = (reused_from_revision_id IS NOT NULL)))
);


--
-- Name: website_sources; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.website_sources (
    source_id uuid NOT NULL,
    root_url character varying(2048) NOT NULL,
    credential_header character varying(127),
    credential_prefix character varying(128),
    credential_id uuid,
    max_concurrency integer NOT NULL,
    requests_per_second integer NOT NULL,
    max_pages integer NOT NULL,
    max_page_bytes integer NOT NULL,
    max_total_bytes bigint NOT NULL,
    max_depth integer NOT NULL,
    poll_interval_seconds integer,
    acquisition_mode character varying(16) DEFAULT 'BUILTIN_CRAWL'::character varying NOT NULL,
    tinyfish_credential_id uuid,
    CONSTRAINT ck_website_sources_acquisition_mode_valid CHECK (((acquisition_mode)::text = ANY ((ARRAY['BUILTIN_CRAWL'::character varying, 'TINYFISH_CRAWL'::character varying, 'DIRECT_JSON_API'::character varying])::text[]))),
    CONSTRAINT ck_website_sources_concurrency_valid CHECK (((max_concurrency >= 1) AND (max_concurrency <= 16))),
    CONSTRAINT ck_website_sources_credential_pair_valid CHECK (((credential_header IS NULL) = (credential_id IS NULL))),
    CONSTRAINT ck_website_sources_depth_valid CHECK (((max_depth >= 0) AND (max_depth <= 10))),
    CONSTRAINT ck_website_sources_direct_json_api_limits_valid CHECK ((((acquisition_mode)::text <> 'DIRECT_JSON_API'::text) OR ((max_pages = 1) AND (max_depth = 0)))),
    CONSTRAINT ck_website_sources_page_bytes_valid CHECK (((max_page_bytes >= 1024) AND (max_page_bytes <= 10485760))),
    CONSTRAINT ck_website_sources_page_limit_valid CHECK (((max_pages >= 1) AND (max_pages <= 10000))),
    CONSTRAINT ck_website_sources_poll_interval_valid CHECK (((poll_interval_seconds IS NULL) OR ((poll_interval_seconds >= 60) AND (poll_interval_seconds <= 604800)))),
    CONSTRAINT ck_website_sources_request_rate_valid CHECK (((requests_per_second >= 1) AND (requests_per_second <= 100))),
    CONSTRAINT ck_website_sources_root_https CHECK ((lower((root_url)::text) ~~ 'https://%'::text)),
    CONSTRAINT ck_website_sources_root_without_query_or_fragment CHECK (((POSITION(('?'::text) IN (root_url)) = 0) AND (POSITION(('#'::text) IN (root_url)) = 0))),
    CONSTRAINT ck_website_sources_tinyfish_credential_only_for_tinyfish CHECK ((((acquisition_mode)::text = 'TINYFISH_CRAWL'::text) OR (tinyfish_credential_id IS NULL))),
    CONSTRAINT ck_website_sources_tinyfish_mode_shape_valid CHECK ((((acquisition_mode)::text <> 'TINYFISH_CRAWL'::text) OR ((tinyfish_credential_id IS NOT NULL) AND (credential_id IS NULL)))),
    CONSTRAINT ck_website_sources_total_bytes_valid CHECK (((max_total_bytes >= max_page_bytes) AND (max_total_bytes <= 1073741824)))
);


--
-- Name: wiki_pages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.wiki_pages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    wiki_version_id uuid NOT NULL,
    slug character varying(255) NOT NULL,
    title character varying(255) NOT NULL,
    description text NOT NULL,
    page_type character varying(255) NOT NULL,
    content_sha256 bytea NOT NULL,
    claims_sha256 bytea NOT NULL,
    body text NOT NULL,
    search_vector tsvector GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, (((title)::text || ' '::text) || body))) STORED,
    CONSTRAINT ck_wiki_pages_claims_sha256_length CHECK ((octet_length(claims_sha256) = 32)),
    CONSTRAINT ck_wiki_pages_content_sha256_length CHECK ((octet_length(content_sha256) = 32))
);


--
-- Name: wiki_versions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.wiki_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    knowledge_base_id uuid NOT NULL,
    documentation_run_id uuid NOT NULL,
    artifact_key character varying(2048) NOT NULL,
    manifest_sha256 bytea NOT NULL,
    page_count integer NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    published_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT ck_wiki_versions_manifest_sha256_length CHECK ((octet_length(manifest_sha256) = 32)),
    CONSTRAINT ck_wiki_versions_page_count_positive CHECK ((page_count > 0))
);


--
-- Name: agent_run_knowledge_bases pk_agent_run_knowledge_bases; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_knowledge_bases
    ADD CONSTRAINT pk_agent_run_knowledge_bases PRIMARY KEY (run_id, "position");


--
-- Name: agent_run_scope_reservations pk_agent_run_scope_reservations; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_scope_reservations
    ADD CONSTRAINT pk_agent_run_scope_reservations PRIMARY KEY (run_id, "position");


--
-- Name: agent_runs pk_agent_runs; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_runs
    ADD CONSTRAINT pk_agent_runs PRIMARY KEY (id);


--
-- Name: agent_version_knowledge_bases pk_agent_version_knowledge_bases; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_version_knowledge_bases
    ADD CONSTRAINT pk_agent_version_knowledge_bases PRIMARY KEY (agent_version_id, "position");


--
-- Name: agent_versions pk_agent_versions; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_versions
    ADD CONSTRAINT pk_agent_versions PRIMARY KEY (id);


--
-- Name: agents pk_agents; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT pk_agents PRIMARY KEY (id);


--
-- Name: audit_events pk_audit_events; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_events
    ADD CONSTRAINT pk_audit_events PRIMARY KEY (id);


--
-- Name: artifact_deletion_intents pk_artifact_deletion_intents; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.artifact_deletion_intents
    ADD CONSTRAINT pk_artifact_deletion_intents PRIMARY KEY (kind, resource_id);


--
-- Name: bootstrap_tokens pk_bootstrap_tokens; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.bootstrap_tokens
    ADD CONSTRAINT pk_bootstrap_tokens PRIMARY KEY (id);


--
-- Name: chat_access_token_agents pk_chat_access_token_agents; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chat_access_token_agents
    ADD CONSTRAINT pk_chat_access_token_agents PRIMARY KEY (token_id, agent_id);


--
-- Name: chat_access_tokens pk_chat_access_tokens; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chat_access_tokens
    ADD CONSTRAINT pk_chat_access_tokens PRIMARY KEY (id);


--
-- Name: channel_bindings pk_channel_bindings; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_bindings
    ADD CONSTRAINT pk_channel_bindings PRIMARY KEY (id);


--
-- Name: channel_binding_triggers pk_channel_binding_triggers; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_binding_triggers
    ADD CONSTRAINT pk_channel_binding_triggers PRIMARY KEY (binding_id, trigger_type);


--
-- Name: claims pk_claims; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.claims
    ADD CONSTRAINT pk_claims PRIMARY KEY (id);


--
-- Name: discord_conversation_messages pk_discord_conversation_messages; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversation_messages
    ADD CONSTRAINT pk_discord_conversation_messages PRIMARY KEY (id);


--
-- Name: discord_conversations pk_discord_conversations; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT pk_discord_conversations PRIMARY KEY (id);


--
-- Name: credential_rotation_attempts pk_credential_rotation_attempts; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_rotation_attempts
    ADD CONSTRAINT pk_credential_rotation_attempts PRIMARY KEY (id);


--
-- Name: credentials pk_credentials; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT pk_credentials PRIMARY KEY (id);


--
-- Name: discord_channels pk_discord_channels; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_channels
    ADD CONSTRAINT pk_discord_channels PRIMARY KEY (connection_id, server_id, channel_id);


--
-- Name: discord_connections pk_discord_connections; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_connections
    ADD CONSTRAINT pk_discord_connections PRIMARY KEY (id);


--
-- Name: discord_roles pk_discord_roles; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_roles
    ADD CONSTRAINT pk_discord_roles PRIMARY KEY (connection_id, server_id, role_id);


--
-- Name: discord_servers pk_discord_servers; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_servers
    ADD CONSTRAINT pk_discord_servers PRIMARY KEY (connection_id, server_id);


--
-- Name: discovery_runs pk_discovery_runs; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discovery_runs
    ADD CONSTRAINT pk_discovery_runs PRIMARY KEY (id);


--
-- Name: documentation_pages pk_documentation_pages; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_pages
    ADD CONSTRAINT pk_documentation_pages PRIMARY KEY (id);


--
-- Name: documentation_run_models pk_documentation_run_models; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_run_models
    ADD CONSTRAINT pk_documentation_run_models PRIMARY KEY (run_id, role);


--
-- Name: documentation_run_sources pk_documentation_run_sources; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_run_sources
    ADD CONSTRAINT pk_documentation_run_sources PRIMARY KEY (run_id, source_id);


--
-- Name: documentation_runs pk_documentation_runs; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_runs
    ADD CONSTRAINT pk_documentation_runs PRIMARY KEY (id);


--
-- Name: event_log pk_event_log; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_log
    ADD CONSTRAINT pk_event_log PRIMARY KEY (sequence);


--
-- Name: event_stream_state pk_event_stream_state; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.event_stream_state
    ADD CONSTRAINT pk_event_stream_state PRIMARY KEY (id);


--
-- Name: evidence pk_evidence; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence
    ADD CONSTRAINT pk_evidence PRIMARY KEY (id);


--
-- Name: idempotency_records pk_idempotency_records; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.idempotency_records
    ADD CONSTRAINT pk_idempotency_records PRIMARY KEY (id);


--
-- Name: job_attempts pk_job_attempts; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.job_attempts
    ADD CONSTRAINT pk_job_attempts PRIMARY KEY (job_id, attempt_number);


--
-- Name: job_events pk_job_events; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.job_events
    ADD CONSTRAINT pk_job_events PRIMARY KEY (sequence);


--
-- Name: jobs pk_jobs; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jobs
    ADD CONSTRAINT pk_jobs PRIMARY KEY (id);


--
-- Name: knowledge_bases pk_knowledge_bases; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.knowledge_bases
    ADD CONSTRAINT pk_knowledge_bases PRIMARY KEY (id);


--
-- Name: model_assignments pk_model_assignments; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_assignments
    ADD CONSTRAINT pk_model_assignments PRIMARY KEY (id);


--
-- Name: model_profile_versions pk_model_profile_versions; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_profile_versions
    ADD CONSTRAINT pk_model_profile_versions PRIMARY KEY (id);


--
-- Name: model_profiles pk_model_profiles; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_profiles
    ADD CONSTRAINT pk_model_profiles PRIMARY KEY (id);


--
-- Name: operator_sessions pk_operator_sessions; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operator_sessions
    ADD CONSTRAINT pk_operator_sessions PRIMARY KEY (id);


--
-- Name: operators pk_operators; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operators
    ADD CONSTRAINT pk_operators PRIMARY KEY (id);


--
-- Name: probe_runs pk_probe_runs; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.probe_runs
    ADD CONSTRAINT pk_probe_runs PRIMARY KEY (id);


--
-- Name: provider_endpoints pk_provider_endpoints; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_endpoints
    ADD CONSTRAINT pk_provider_endpoints PRIMARY KEY (id);


--
-- Name: rate_limit_buckets pk_rate_limit_buckets; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rate_limit_buckets
    ADD CONSTRAINT pk_rate_limit_buckets PRIMARY KEY (binding_id, external_user_id, window_started_at);


--
-- Name: repository_sources pk_repository_sources; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_sources
    ADD CONSTRAINT pk_repository_sources PRIMARY KEY (source_id);


--
-- Name: source_revisions pk_source_revisions; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_revisions
    ADD CONSTRAINT pk_source_revisions PRIMARY KEY (id);


--
-- Name: source_syncs pk_source_syncs; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_syncs
    ADD CONSTRAINT pk_source_syncs PRIMARY KEY (id);


--
-- Name: sources pk_sources; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sources
    ADD CONSTRAINT pk_sources PRIMARY KEY (id);


--
-- Name: website_revision_pages pk_website_revision_pages; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.website_revision_pages
    ADD CONSTRAINT pk_website_revision_pages PRIMARY KEY (source_id, revision_id, canonical_url);


--
-- Name: website_sources pk_website_sources; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.website_sources
    ADD CONSTRAINT pk_website_sources PRIMARY KEY (source_id);


--
-- Name: wiki_pages pk_wiki_pages; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wiki_pages
    ADD CONSTRAINT pk_wiki_pages PRIMARY KEY (id);


--
-- Name: wiki_versions pk_wiki_versions; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wiki_versions
    ADD CONSTRAINT pk_wiki_versions PRIMARY KEY (id);


--
-- Name: agent_run_knowledge_bases uq_agent_run_knowledge_bases_run_knowledge_base; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_knowledge_bases
    ADD CONSTRAINT uq_agent_run_knowledge_bases_run_knowledge_base UNIQUE (run_id, knowledge_base_id);


--
-- Name: agent_run_scope_reservations uq_agent_run_scope_reservations_run_knowledge_base; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_scope_reservations
    ADD CONSTRAINT uq_agent_run_scope_reservations_run_knowledge_base UNIQUE (run_id, knowledge_base_id);


--
-- Name: agent_version_knowledge_bases uq_agent_version_knowledge_bases_version_knowledge_base; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_version_knowledge_bases
    ADD CONSTRAINT uq_agent_version_knowledge_bases_version_knowledge_base UNIQUE (agent_version_id, knowledge_base_id);


--
-- Name: agent_versions uq_agent_versions_agent_id_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_versions
    ADD CONSTRAINT uq_agent_versions_agent_id_id UNIQUE (agent_id, id);


--
-- Name: agent_versions uq_agent_versions_agent_version_number; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_versions
    ADD CONSTRAINT uq_agent_versions_agent_version_number UNIQUE (agent_id, version_number);


--
-- Name: agents uq_agents_agent_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT uq_agents_agent_key UNIQUE (agent_key);


--
-- Name: chat_access_tokens uq_chat_access_tokens_token_digest; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chat_access_tokens
    ADD CONSTRAINT uq_chat_access_tokens_token_digest UNIQUE (token_digest);


--
-- Name: channel_bindings uq_channel_bindings_id_agent; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_bindings
    ADD CONSTRAINT uq_channel_bindings_id_agent UNIQUE (id, agent_id);


--
-- Name: channel_bindings uq_channel_bindings_route_state; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_bindings
    ADD CONSTRAINT uq_channel_bindings_route_state UNIQUE (id, connection_id, server_id, listen_channel_id, enabled);


--
-- Name: claims uq_claims_version_stable_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.claims
    ADD CONSTRAINT uq_claims_version_stable_id UNIQUE (wiki_version_id, stable_id);


--
-- Name: discord_connections uq_discord_connections_display_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_connections
    ADD CONSTRAINT uq_discord_connections_display_key UNIQUE (display_key);


--
-- Name: discord_connections uq_discord_connections_credential_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_connections
    ADD CONSTRAINT uq_discord_connections_credential_id UNIQUE (credential_id);


--
-- Name: discord_conversation_messages uq_discord_conversation_messages_sequence; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversation_messages
    ADD CONSTRAINT uq_discord_conversation_messages_sequence UNIQUE (conversation_id, sequence);


--
-- Name: discord_conversations uq_discord_conversations_identity; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT uq_discord_conversations_identity UNIQUE (binding_id, agent_id, agent_version_id, external_user_id, destination_id);


--
-- Name: discovery_runs uq_discovery_runs_job_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discovery_runs
    ADD CONSTRAINT uq_discovery_runs_job_id UNIQUE (job_id);


--
-- Name: documentation_pages uq_documentation_pages_job_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_pages
    ADD CONSTRAINT uq_documentation_pages_job_id UNIQUE (job_id);


--
-- Name: documentation_pages uq_documentation_pages_run_position; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_pages
    ADD CONSTRAINT uq_documentation_pages_run_position UNIQUE (run_id, "position");


--
-- Name: documentation_pages uq_documentation_pages_run_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_pages
    ADD CONSTRAINT uq_documentation_pages_run_slug UNIQUE (run_id, slug);


--
-- Name: documentation_runs uq_documentation_runs_prepare_job_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_runs
    ADD CONSTRAINT uq_documentation_runs_prepare_job_id UNIQUE (prepare_job_id);


--
-- Name: documentation_runs uq_documentation_runs_knowledge_base_id_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_runs
    ADD CONSTRAINT uq_documentation_runs_knowledge_base_id_id UNIQUE (knowledge_base_id, id);


--
-- Name: evidence uq_evidence_claim_evidence_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence
    ADD CONSTRAINT uq_evidence_claim_evidence_id UNIQUE (claim_id, evidence_id);


--
-- Name: idempotency_records uq_idempotency_records_scope_request_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.idempotency_records
    ADD CONSTRAINT uq_idempotency_records_scope_request_key UNIQUE (scope, request_key);


--
-- Name: job_attempts uq_job_attempts_job_lease_generation; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.job_attempts
    ADD CONSTRAINT uq_job_attempts_job_lease_generation UNIQUE (job_id, lease_generation);


--
-- Name: model_assignments uq_model_assignments_knowledge_base_role; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_assignments
    ADD CONSTRAINT uq_model_assignments_knowledge_base_role UNIQUE (knowledge_base_id, role);


--
-- Name: model_profile_versions uq_model_profile_versions_profile_id_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_profile_versions
    ADD CONSTRAINT uq_model_profile_versions_profile_id_id UNIQUE (profile_id, id);


--
-- Name: model_profile_versions uq_model_profile_versions_profile_version_number; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_profile_versions
    ADD CONSTRAINT uq_model_profile_versions_profile_version_number UNIQUE (profile_id, version_number);


--
-- Name: model_profiles uq_model_profiles_endpoint_model_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_profiles
    ADD CONSTRAINT uq_model_profiles_endpoint_model_id UNIQUE (endpoint_id, model_id);


--
-- Name: operator_sessions uq_operator_sessions_token_digest; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operator_sessions
    ADD CONSTRAINT uq_operator_sessions_token_digest UNIQUE (token_digest);


--
-- Name: operators uq_operators_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operators
    ADD CONSTRAINT uq_operators_username_key UNIQUE (username_key);


--
-- Name: probe_runs uq_probe_runs_job_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.probe_runs
    ADD CONSTRAINT uq_probe_runs_job_id UNIQUE (job_id);


--
-- Name: source_revisions uq_source_revisions_artifact_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_revisions
    ADD CONSTRAINT uq_source_revisions_artifact_key UNIQUE (artifact_key);


--
-- Name: source_revisions uq_source_revisions_source_id_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_revisions
    ADD CONSTRAINT uq_source_revisions_source_id_id UNIQUE (source_id, id);


--
-- Name: source_syncs uq_source_syncs_job_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_syncs
    ADD CONSTRAINT uq_source_syncs_job_id UNIQUE (job_id);


--
-- Name: website_revision_pages uq_website_revision_pages_evidence_uri; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.website_revision_pages
    ADD CONSTRAINT uq_website_revision_pages_evidence_uri UNIQUE (evidence_uri);


--
-- Name: wiki_pages uq_wiki_pages_version_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wiki_pages
    ADD CONSTRAINT uq_wiki_pages_version_id UNIQUE (wiki_version_id, id);


--
-- Name: wiki_pages uq_wiki_pages_version_slug; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wiki_pages
    ADD CONSTRAINT uq_wiki_pages_version_slug UNIQUE (wiki_version_id, slug);


--
-- Name: wiki_versions uq_wiki_versions_artifact_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wiki_versions
    ADD CONSTRAINT uq_wiki_versions_artifact_key UNIQUE (artifact_key);


--
-- Name: wiki_versions uq_wiki_versions_documentation_run_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wiki_versions
    ADD CONSTRAINT uq_wiki_versions_documentation_run_id UNIQUE (documentation_run_id);


--
-- Name: wiki_versions uq_wiki_versions_knowledge_base_id_id; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wiki_versions
    ADD CONSTRAINT uq_wiki_versions_knowledge_base_id_id UNIQUE (knowledge_base_id, id);


--
-- Name: ix_agent_run_knowledge_bases_knowledge_base; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_agent_run_knowledge_bases_knowledge_base ON public.agent_run_knowledge_bases USING btree (knowledge_base_id, run_id);


--
-- Name: ix_agent_run_knowledge_bases_wiki; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_agent_run_knowledge_bases_wiki ON public.agent_run_knowledge_bases USING btree (wiki_version_id, knowledge_base_id, run_id);


--
-- Name: ix_agent_run_scope_reservations_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_agent_run_scope_reservations_expiry ON public.agent_run_scope_reservations USING btree (expires_at, run_id, "position");


--
-- Name: ix_agent_run_scope_reservations_wiki; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_agent_run_scope_reservations_wiki ON public.agent_run_scope_reservations USING btree (knowledge_base_id, wiki_version_id, expires_at);


--
-- Name: ix_agent_runs_agent_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_agent_runs_agent_created ON public.agent_runs USING btree (agent_id, created_at DESC, id DESC);


--
-- Name: ix_agent_runs_completed; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_agent_runs_completed ON public.agent_runs USING btree (completed_at, id);


--
-- Name: ix_agent_runs_failed_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_agent_runs_failed_created ON public.agent_runs USING btree (created_at DESC, id) WHERE ((outcome)::text = 'FAILED'::text);


--
-- Name: ix_agent_version_knowledge_bases_knowledge_base; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_agent_version_knowledge_bases_knowledge_base ON public.agent_version_knowledge_bases USING btree (knowledge_base_id, agent_version_id);


--
-- Name: ix_agents_lifecycle_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_agents_lifecycle_created ON public.agents USING btree (lifecycle, created_at, id);


--
-- Name: ix_chat_access_token_agents_agent; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_chat_access_token_agents_agent ON public.chat_access_token_agents USING btree (agent_id, token_id);


--
-- Name: ix_chat_access_tokens_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_chat_access_tokens_created ON public.chat_access_tokens USING btree (created_at DESC, id DESC);


--
-- Name: ix_chat_access_tokens_live_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_chat_access_tokens_live_expiry ON public.chat_access_tokens USING btree (expires_at, id) WHERE (revoked_at IS NULL);


--
-- Name: ix_audit_events_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_audit_events_request_id ON public.audit_events USING btree (request_id);


--
-- Name: ix_audit_events_target; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_audit_events_target ON public.audit_events USING btree (target_type, target_id);


--
-- Name: ix_artifact_deletion_intents_owner; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_artifact_deletion_intents_owner ON public.artifact_deletion_intents USING btree (scope_id, kind);


--
-- Name: ix_channel_bindings_connection_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_channel_bindings_connection_enabled ON public.channel_bindings USING btree (connection_id, enabled) WHERE (deleted_at IS NULL);


--
-- Name: uq_channel_binding_triggers_enabled_route; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_channel_binding_triggers_enabled_route ON public.channel_binding_triggers USING btree (connection_id, server_id, listen_channel_id, trigger_type) WHERE enabled;


--
-- Name: ix_claims_search; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_claims_search ON public.claims USING gin (search_vector);


--
-- Name: ix_discord_conversation_messages_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_discord_conversation_messages_order ON public.discord_conversation_messages USING btree (conversation_id, sequence);


--
-- Name: ix_discord_conversations_expiry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_discord_conversations_expiry ON public.discord_conversations USING btree (expires_at, id);


--
-- Name: uq_discord_connections_application_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_discord_connections_application_id ON public.discord_connections USING btree (application_id) WHERE (application_id IS NOT NULL);


--
-- Name: uq_discord_connections_bot_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_discord_connections_bot_user_id ON public.discord_connections USING btree (bot_user_id) WHERE (bot_user_id IS NOT NULL);


--
-- Name: ix_discovery_runs_endpoint_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_discovery_runs_endpoint_created ON public.discovery_runs USING btree (endpoint_id, created_at);


--
-- Name: ix_documentation_pages_run_position; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_documentation_pages_run_position ON public.documentation_pages USING btree (run_id, "position");


--
-- Name: ix_documentation_runs_knowledge_base_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_documentation_runs_knowledge_base_created ON public.documentation_runs USING btree (knowledge_base_id, created_at);


--
-- Name: ix_event_log_resource; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_event_log_resource ON public.event_log USING btree (resource_type, resource_id);


--
-- Name: ix_event_log_created_sequence; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_event_log_created_sequence ON public.event_log USING btree (created_at, sequence);


--
-- Name: ix_job_events_job_sequence; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_job_events_job_sequence ON public.job_events USING btree (job_id, sequence);


--
-- Name: ix_jobs_claim; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_jobs_claim ON public.jobs USING btree (status, not_before, created_at);


--
-- Name: ix_jobs_active_concurrency; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_jobs_active_concurrency ON public.jobs USING btree (concurrency_key, status, lease_expires_at) WHERE ((concurrency_key <> ''::text) AND ((status)::text = ANY ((ARRAY['LEASED'::character varying, 'CANCEL_REQUESTED'::character varying])::text[])));


--
-- Name: ix_probe_runs_profile_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_probe_runs_profile_created ON public.probe_runs USING btree (model_profile_id, created_at);


--
-- Name: ix_source_revisions_source_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_source_revisions_source_created ON public.source_revisions USING btree (source_id, created_at);


--
-- Name: ix_source_syncs_source_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_source_syncs_source_created ON public.source_syncs USING btree (source_id, created_at);


--
-- Name: ix_sources_knowledge_base_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_sources_knowledge_base_created ON public.sources USING btree (knowledge_base_id, created_at);


--
-- Name: ix_website_revision_pages_revision; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_website_revision_pages_revision ON public.website_revision_pages USING btree (source_id, revision_id);


--
-- Name: ix_wiki_pages_search; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_wiki_pages_search ON public.wiki_pages USING gin (search_vector);


--
-- Name: ix_wiki_pages_version; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_wiki_pages_version ON public.wiki_pages USING btree (wiki_version_id);


--
-- Name: ix_wiki_versions_knowledge_base_published; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX ix_wiki_versions_knowledge_base_published ON public.wiki_versions USING btree (knowledge_base_id, published_at);


--
-- Name: uq_jobs_active_operation_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_jobs_active_operation_key ON public.jobs USING btree (operation_key) WHERE ((status)::text = ANY ((ARRAY['PENDING'::character varying, 'LEASED'::character varying, 'RETRY_WAIT'::character varying, 'CANCEL_REQUESTED'::character varying])::text[]));


--
-- Name: uq_knowledge_bases_live_name_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_knowledge_bases_live_name_key ON public.knowledge_bases USING btree (name_key) WHERE (deleted_at IS NULL);


--
-- Name: uq_provider_endpoints_active_display_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_provider_endpoints_active_display_key ON public.provider_endpoints USING btree (display_key) WHERE ((lifecycle)::text = 'ACTIVE'::text);


--
-- Name: uq_source_revisions_active_native_fingerprint; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_source_revisions_active_native_fingerprint ON public.source_revisions USING btree (source_id, native_version, fingerprint) WHERE (artifact_purged_at IS NULL);


--
-- Name: uq_sources_live_knowledge_base_display_key; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_sources_live_knowledge_base_display_key ON public.sources USING btree (knowledge_base_id, display_key) WHERE ((lifecycle)::text <> 'REMOVED'::text);


--
-- Name: agent_run_knowledge_bases fk_agent_run_knowledge_bases_documentation_run; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_knowledge_bases
    ADD CONSTRAINT fk_agent_run_knowledge_bases_documentation_run FOREIGN KEY (knowledge_base_id, documentation_run_id) REFERENCES public.documentation_runs(knowledge_base_id, id) ON DELETE RESTRICT;


--
-- Name: agent_run_knowledge_bases fk_agent_run_knowledge_bases_run_id_agent_runs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_knowledge_bases
    ADD CONSTRAINT fk_agent_run_knowledge_bases_run_id_agent_runs FOREIGN KEY (run_id) REFERENCES public.agent_runs(id) ON DELETE CASCADE;


--
-- Name: agent_run_knowledge_bases fk_agent_run_knowledge_bases_wiki_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_knowledge_bases
    ADD CONSTRAINT fk_agent_run_knowledge_bases_wiki_version FOREIGN KEY (knowledge_base_id, wiki_version_id) REFERENCES public.wiki_versions(knowledge_base_id, id) ON DELETE RESTRICT;


--
-- Name: agent_run_scope_reservations fk_agent_run_scope_reservations_wiki_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_run_scope_reservations
    ADD CONSTRAINT fk_agent_run_scope_reservations_wiki_version FOREIGN KEY (knowledge_base_id, wiki_version_id) REFERENCES public.wiki_versions(knowledge_base_id, id) ON DELETE RESTRICT;


--
-- Name: agent_runs fk_agent_runs_agent_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_runs
    ADD CONSTRAINT fk_agent_runs_agent_version FOREIGN KEY (agent_id, agent_version_id) REFERENCES public.agent_versions(agent_id, id) ON DELETE RESTRICT;


--
-- Name: agent_runs fk_agent_runs_captured_credential_id_credentials; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_runs
    ADD CONSTRAINT fk_agent_runs_captured_credential_id_credentials FOREIGN KEY (captured_credential_id) REFERENCES public.credentials(id) ON DELETE RESTRICT;


--
-- Name: agent_runs fk_agent_runs_model_profile_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_runs
    ADD CONSTRAINT fk_agent_runs_model_profile_version FOREIGN KEY (model_profile_id, model_profile_version_id) REFERENCES public.model_profile_versions(profile_id, id) ON DELETE RESTRICT;


--
-- Name: agent_runs fk_agent_runs_provider_endpoint_id_provider_endpoints; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_runs
    ADD CONSTRAINT fk_agent_runs_provider_endpoint_id_provider_endpoints FOREIGN KEY (provider_endpoint_id) REFERENCES public.provider_endpoints(id) ON DELETE RESTRICT;


--
-- Name: agent_version_knowledge_bases fk_agent_version_knowledge_bases_knowledge_base; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_version_knowledge_bases
    ADD CONSTRAINT fk_agent_version_knowledge_bases_knowledge_base FOREIGN KEY (knowledge_base_id) REFERENCES public.knowledge_bases(id) ON DELETE RESTRICT;


--
-- Name: agent_version_knowledge_bases fk_agent_version_knowledge_bases_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_version_knowledge_bases
    ADD CONSTRAINT fk_agent_version_knowledge_bases_version FOREIGN KEY (agent_id, agent_version_id) REFERENCES public.agent_versions(agent_id, id) ON DELETE CASCADE;


--
-- Name: agent_versions fk_agent_versions_agent_id_agents; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_versions
    ADD CONSTRAINT fk_agent_versions_agent_id_agents FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED;


--
-- Name: agent_versions fk_agent_versions_created_by_operator_id_operators; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_versions
    ADD CONSTRAINT fk_agent_versions_created_by_operator_id_operators FOREIGN KEY (created_by_operator_id) REFERENCES public.operators(id) ON DELETE RESTRICT;


--
-- Name: agent_versions fk_agent_versions_model_profile_id_model_profiles; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agent_versions
    ADD CONSTRAINT fk_agent_versions_model_profile_id_model_profiles FOREIGN KEY (model_profile_id) REFERENCES public.model_profiles(id) ON DELETE RESTRICT;


--
-- Name: agents fk_agents_current_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.agents
    ADD CONSTRAINT fk_agents_current_version FOREIGN KEY (id, current_version_id) REFERENCES public.agent_versions(agent_id, id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: chat_access_token_agents fk_chat_access_token_agents_agent_id_agents; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chat_access_token_agents
    ADD CONSTRAINT fk_chat_access_token_agents_agent_id_agents FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE RESTRICT;


--
-- Name: chat_access_token_agents fk_chat_access_token_agents_token_id_tokens; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chat_access_token_agents
    ADD CONSTRAINT fk_chat_access_token_agents_token_id_tokens FOREIGN KEY (token_id) REFERENCES public.chat_access_tokens(id) ON DELETE CASCADE;


--
-- Name: chat_access_tokens fk_chat_access_tokens_created_by_operator_id_operators; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.chat_access_tokens
    ADD CONSTRAINT fk_chat_access_tokens_created_by_operator_id_operators FOREIGN KEY (created_by_operator_id) REFERENCES public.operators(id) ON DELETE RESTRICT;


--
-- Name: channel_bindings fk_channel_bindings_connection_id_discord_connections; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_bindings
    ADD CONSTRAINT fk_channel_bindings_connection_id_discord_connections FOREIGN KEY (connection_id) REFERENCES public.discord_connections(id) ON DELETE CASCADE;


--
-- Name: channel_bindings fk_channel_bindings_agent_id_agents; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_bindings
    ADD CONSTRAINT fk_channel_bindings_agent_id_agents FOREIGN KEY (agent_id) REFERENCES public.agents(id) ON DELETE RESTRICT;


--
-- Name: channel_bindings fk_channel_bindings_listen_channel; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_bindings
    ADD CONSTRAINT fk_channel_bindings_listen_channel FOREIGN KEY (connection_id, server_id, listen_channel_id) REFERENCES public.discord_channels(connection_id, server_id, channel_id) ON DELETE RESTRICT;


--
-- Name: channel_bindings fk_channel_bindings_reply_channel; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_bindings
    ADD CONSTRAINT fk_channel_bindings_reply_channel FOREIGN KEY (connection_id, server_id, reply_channel_id) REFERENCES public.discord_channels(connection_id, server_id, channel_id) ON DELETE RESTRICT;


--
-- Name: channel_binding_triggers fk_channel_binding_triggers_route_state; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.channel_binding_triggers
    ADD CONSTRAINT fk_channel_binding_triggers_route_state FOREIGN KEY (binding_id, connection_id, server_id, listen_channel_id, enabled) REFERENCES public.channel_bindings(id, connection_id, server_id, listen_channel_id, enabled) ON UPDATE CASCADE ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED;


--
-- Name: claims fk_claims_wiki_page; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.claims
    ADD CONSTRAINT fk_claims_wiki_page FOREIGN KEY (wiki_version_id, wiki_page_id) REFERENCES public.wiki_pages(wiki_version_id, id) ON DELETE CASCADE;


--
-- Name: claims fk_claims_wiki_version_id_wiki_versions; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.claims
    ADD CONSTRAINT fk_claims_wiki_version_id_wiki_versions FOREIGN KEY (wiki_version_id) REFERENCES public.wiki_versions(id) ON DELETE CASCADE;


--
-- Name: discord_conversation_messages fk_discord_conversation_messages_conversation; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversation_messages
    ADD CONSTRAINT fk_discord_conversation_messages_conversation FOREIGN KEY (conversation_id) REFERENCES public.discord_conversations(id) ON DELETE CASCADE;


--
-- Name: discord_conversations fk_discord_conversations_agent_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT fk_discord_conversations_agent_version FOREIGN KEY (agent_id, agent_version_id) REFERENCES public.agent_versions(agent_id, id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;


--
-- Name: discord_conversations fk_discord_conversations_binding_agent; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_conversations
    ADD CONSTRAINT fk_discord_conversations_binding_agent FOREIGN KEY (binding_id, agent_id) REFERENCES public.channel_bindings(id, agent_id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED;


--
-- Name: credential_rotation_attempts fk_credential_rotation_attempts_actor_operator_id_operators; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_rotation_attempts
    ADD CONSTRAINT fk_credential_rotation_attempts_actor_operator_id_operators FOREIGN KEY (actor_operator_id) REFERENCES public.operators(id) ON DELETE RESTRICT;


--
-- Name: credential_rotation_attempts fk_credential_rotation_attempts_credential_id_credentials; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_rotation_attempts
    ADD CONSTRAINT fk_credential_rotation_attempts_credential_id_credentials FOREIGN KEY (credential_id) REFERENCES public.credentials(id) ON DELETE CASCADE;


--
-- Name: discord_channels fk_discord_channels_server; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_channels
    ADD CONSTRAINT fk_discord_channels_server FOREIGN KEY (connection_id, server_id) REFERENCES public.discord_servers(connection_id, server_id) ON DELETE CASCADE;


--
-- Name: discord_connections fk_discord_connections_credential_id_credentials; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_connections
    ADD CONSTRAINT fk_discord_connections_credential_id_credentials FOREIGN KEY (credential_id) REFERENCES public.credentials(id) ON DELETE RESTRICT;


--
-- Name: discord_roles fk_discord_roles_server; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_roles
    ADD CONSTRAINT fk_discord_roles_server FOREIGN KEY (connection_id, server_id) REFERENCES public.discord_servers(connection_id, server_id) ON DELETE CASCADE;


--
-- Name: discord_servers fk_discord_servers_connection_id_discord_connections; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discord_servers
    ADD CONSTRAINT fk_discord_servers_connection_id_discord_connections FOREIGN KEY (connection_id) REFERENCES public.discord_connections(id) ON DELETE CASCADE;


--
-- Name: discovery_runs fk_discovery_runs_endpoint_id_provider_endpoints; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discovery_runs
    ADD CONSTRAINT fk_discovery_runs_endpoint_id_provider_endpoints FOREIGN KEY (endpoint_id) REFERENCES public.provider_endpoints(id) ON DELETE CASCADE;


--
-- Name: discovery_runs fk_discovery_runs_job_id_jobs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discovery_runs
    ADD CONSTRAINT fk_discovery_runs_job_id_jobs FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE RESTRICT;


--
-- Name: discovery_runs fk_discovery_runs_requested_by_operator_id_operators; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.discovery_runs
    ADD CONSTRAINT fk_discovery_runs_requested_by_operator_id_operators FOREIGN KEY (requested_by_operator_id) REFERENCES public.operators(id) ON DELETE RESTRICT;


--
-- Name: documentation_pages fk_documentation_pages_job_id_jobs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_pages
    ADD CONSTRAINT fk_documentation_pages_job_id_jobs FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE RESTRICT;


--
-- Name: documentation_pages fk_documentation_pages_run_id_documentation_runs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_pages
    ADD CONSTRAINT fk_documentation_pages_run_id_documentation_runs FOREIGN KEY (run_id) REFERENCES public.documentation_runs(id) ON DELETE CASCADE;


--
-- Name: documentation_run_models fk_documentation_run_models_model_profile_id_model_profiles; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_run_models
    ADD CONSTRAINT fk_documentation_run_models_model_profile_id_model_profiles FOREIGN KEY (model_profile_id) REFERENCES public.model_profiles(id) ON DELETE RESTRICT;


--
-- Name: documentation_run_models fk_documentation_run_models_profile_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_run_models
    ADD CONSTRAINT fk_documentation_run_models_profile_version FOREIGN KEY (model_profile_id, model_profile_version_id) REFERENCES public.model_profile_versions(profile_id, id) ON DELETE RESTRICT;


--
-- Name: documentation_run_models fk_documentation_run_models_provider_endpoint_id_provid_d517; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_run_models
    ADD CONSTRAINT fk_documentation_run_models_provider_endpoint_id_provid_d517 FOREIGN KEY (provider_endpoint_id) REFERENCES public.provider_endpoints(id) ON DELETE RESTRICT;


--
-- Name: documentation_run_models fk_documentation_run_models_run_id_documentation_runs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_run_models
    ADD CONSTRAINT fk_documentation_run_models_run_id_documentation_runs FOREIGN KEY (run_id) REFERENCES public.documentation_runs(id) ON DELETE CASCADE;


--
-- Name: documentation_run_sources fk_documentation_run_sources_revision; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_run_sources
    ADD CONSTRAINT fk_documentation_run_sources_revision FOREIGN KEY (source_id, source_revision_id) REFERENCES public.source_revisions(source_id, id) ON DELETE RESTRICT;


--
-- Name: documentation_run_sources fk_documentation_run_sources_run_id_documentation_runs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_run_sources
    ADD CONSTRAINT fk_documentation_run_sources_run_id_documentation_runs FOREIGN KEY (run_id) REFERENCES public.documentation_runs(id) ON DELETE CASCADE;


--
-- Name: documentation_run_sources fk_documentation_run_sources_source_id_sources; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_run_sources
    ADD CONSTRAINT fk_documentation_run_sources_source_id_sources FOREIGN KEY (source_id) REFERENCES public.sources(id) ON DELETE RESTRICT;


--
-- Name: documentation_runs fk_documentation_runs_knowledge_base_id_knowledge_bases; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_runs
    ADD CONSTRAINT fk_documentation_runs_knowledge_base_id_knowledge_bases FOREIGN KEY (knowledge_base_id) REFERENCES public.knowledge_bases(id) ON DELETE RESTRICT;


--
-- Name: documentation_runs fk_documentation_runs_prepare_job_id_jobs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_runs
    ADD CONSTRAINT fk_documentation_runs_prepare_job_id_jobs FOREIGN KEY (prepare_job_id) REFERENCES public.jobs(id) ON DELETE RESTRICT;


--
-- Name: documentation_runs fk_documentation_runs_prior_wiki_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_runs
    ADD CONSTRAINT fk_documentation_runs_prior_wiki_version FOREIGN KEY (prior_wiki_version_id) REFERENCES public.wiki_versions(id) ON DELETE RESTRICT;


--
-- Name: documentation_runs fk_documentation_runs_published_wiki_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.documentation_runs
    ADD CONSTRAINT fk_documentation_runs_published_wiki_version FOREIGN KEY (published_wiki_version_id) REFERENCES public.wiki_versions(id) ON DELETE RESTRICT;


--
-- Name: evidence fk_evidence_claim_id_claims; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence
    ADD CONSTRAINT fk_evidence_claim_id_claims FOREIGN KEY (claim_id) REFERENCES public.claims(id) ON DELETE CASCADE;


--
-- Name: evidence fk_evidence_source_id_sources; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence
    ADD CONSTRAINT fk_evidence_source_id_sources FOREIGN KEY (source_id) REFERENCES public.sources(id) ON DELETE RESTRICT;


--
-- Name: evidence fk_evidence_source_revision; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.evidence
    ADD CONSTRAINT fk_evidence_source_revision FOREIGN KEY (source_id, source_revision_id) REFERENCES public.source_revisions(source_id, id) ON DELETE RESTRICT;


--
-- Name: job_attempts fk_job_attempts_job_id_jobs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.job_attempts
    ADD CONSTRAINT fk_job_attempts_job_id_jobs FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;


--
-- Name: job_events fk_job_events_job_id_jobs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.job_events
    ADD CONSTRAINT fk_job_events_job_id_jobs FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE CASCADE;


--
-- Name: knowledge_bases fk_knowledge_bases_published_wiki; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.knowledge_bases
    ADD CONSTRAINT fk_knowledge_bases_published_wiki FOREIGN KEY (published_wiki_id) REFERENCES public.wiki_versions(id) ON DELETE RESTRICT;


--
-- Name: model_assignments fk_model_assignments_knowledge_base_id_knowledge_bases; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_assignments
    ADD CONSTRAINT fk_model_assignments_knowledge_base_id_knowledge_bases FOREIGN KEY (knowledge_base_id) REFERENCES public.knowledge_bases(id) ON DELETE CASCADE;


--
-- Name: model_assignments fk_model_assignments_model_profile_id_model_profiles; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_assignments
    ADD CONSTRAINT fk_model_assignments_model_profile_id_model_profiles FOREIGN KEY (model_profile_id) REFERENCES public.model_profiles(id) ON DELETE RESTRICT;


--
-- Name: model_profile_versions fk_model_profile_versions_created_by_operator_id_operators; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_profile_versions
    ADD CONSTRAINT fk_model_profile_versions_created_by_operator_id_operators FOREIGN KEY (created_by_operator_id) REFERENCES public.operators(id) ON DELETE RESTRICT;


--
-- Name: model_profile_versions fk_model_profile_versions_profile_id_model_profiles; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_profile_versions
    ADD CONSTRAINT fk_model_profile_versions_profile_id_model_profiles FOREIGN KEY (profile_id) REFERENCES public.model_profiles(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED;


--
-- Name: model_profiles fk_model_profiles_current_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_profiles
    ADD CONSTRAINT fk_model_profiles_current_version FOREIGN KEY (id, current_version_id) REFERENCES public.model_profile_versions(profile_id, id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: model_profiles fk_model_profiles_endpoint_id_provider_endpoints; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_profiles
    ADD CONSTRAINT fk_model_profiles_endpoint_id_provider_endpoints FOREIGN KEY (endpoint_id) REFERENCES public.provider_endpoints(id) ON DELETE CASCADE;


--
-- Name: operator_sessions fk_operator_sessions_operator_id_operators; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.operator_sessions
    ADD CONSTRAINT fk_operator_sessions_operator_id_operators FOREIGN KEY (operator_id) REFERENCES public.operators(id) ON DELETE CASCADE;


--
-- Name: probe_runs fk_probe_runs_captured_profile_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.probe_runs
    ADD CONSTRAINT fk_probe_runs_captured_profile_version FOREIGN KEY (model_profile_id, captured_profile_version_id) REFERENCES public.model_profile_versions(profile_id, id) ON DELETE RESTRICT;


--
-- Name: probe_runs fk_probe_runs_job_id_jobs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.probe_runs
    ADD CONSTRAINT fk_probe_runs_job_id_jobs FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE RESTRICT;


--
-- Name: probe_runs fk_probe_runs_model_profile_id_model_profiles; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.probe_runs
    ADD CONSTRAINT fk_probe_runs_model_profile_id_model_profiles FOREIGN KEY (model_profile_id) REFERENCES public.model_profiles(id) ON DELETE CASCADE;


--
-- Name: probe_runs fk_probe_runs_requested_by_operator_id_operators; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.probe_runs
    ADD CONSTRAINT fk_probe_runs_requested_by_operator_id_operators FOREIGN KEY (requested_by_operator_id) REFERENCES public.operators(id) ON DELETE RESTRICT;


--
-- Name: probe_runs fk_probe_runs_resulting_profile_version; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.probe_runs
    ADD CONSTRAINT fk_probe_runs_resulting_profile_version FOREIGN KEY (model_profile_id, resulting_version_id) REFERENCES public.model_profile_versions(profile_id, id) ON DELETE RESTRICT;


--
-- Name: provider_endpoints fk_provider_endpoints_credential_id_credentials; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_endpoints
    ADD CONSTRAINT fk_provider_endpoints_credential_id_credentials FOREIGN KEY (credential_id) REFERENCES public.credentials(id) ON DELETE RESTRICT;


--
-- Name: rate_limit_buckets fk_rate_limit_buckets_binding_id_channel_bindings; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.rate_limit_buckets
    ADD CONSTRAINT fk_rate_limit_buckets_binding_id_channel_bindings FOREIGN KEY (binding_id) REFERENCES public.channel_bindings(id) ON DELETE CASCADE;


--
-- Name: repository_sources fk_repository_sources_credential_id_credentials; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_sources
    ADD CONSTRAINT fk_repository_sources_credential_id_credentials FOREIGN KEY (credential_id) REFERENCES public.credentials(id) ON DELETE RESTRICT;


--
-- Name: repository_sources fk_repository_sources_source_id_sources; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.repository_sources
    ADD CONSTRAINT fk_repository_sources_source_id_sources FOREIGN KEY (source_id) REFERENCES public.sources(id) ON DELETE CASCADE;


--
-- Name: source_revisions fk_source_revisions_source_id_sources; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_revisions
    ADD CONSTRAINT fk_source_revisions_source_id_sources FOREIGN KEY (source_id) REFERENCES public.sources(id) ON DELETE RESTRICT;


--
-- Name: source_syncs fk_source_syncs_captured_credential_id_credentials; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_syncs
    ADD CONSTRAINT fk_source_syncs_captured_credential_id_credentials FOREIGN KEY (captured_credential_id) REFERENCES public.credentials(id) ON DELETE RESTRICT;


--
-- Name: source_syncs fk_source_syncs_captured_tinyfish_credential_id_credentials; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_syncs
    ADD CONSTRAINT fk_source_syncs_captured_tinyfish_credential_id_credentials FOREIGN KEY (captured_tinyfish_credential_id) REFERENCES public.credentials(id) ON DELETE RESTRICT;


--
-- Name: source_syncs fk_source_syncs_job_id_jobs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_syncs
    ADD CONSTRAINT fk_source_syncs_job_id_jobs FOREIGN KEY (job_id) REFERENCES public.jobs(id) ON DELETE RESTRICT;


--
-- Name: source_syncs fk_source_syncs_previous_revision; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_syncs
    ADD CONSTRAINT fk_source_syncs_previous_revision FOREIGN KEY (source_id, captured_previous_revision_id) REFERENCES public.source_revisions(source_id, id) ON DELETE RESTRICT;


--
-- Name: source_syncs fk_source_syncs_requested_by_operator_id_operators; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_syncs
    ADD CONSTRAINT fk_source_syncs_requested_by_operator_id_operators FOREIGN KEY (requested_by_operator_id) REFERENCES public.operators(id) ON DELETE RESTRICT;


--
-- Name: source_syncs fk_source_syncs_result_revision; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_syncs
    ADD CONSTRAINT fk_source_syncs_result_revision FOREIGN KEY (source_id, result_revision_id) REFERENCES public.source_revisions(source_id, id) ON DELETE RESTRICT;


--
-- Name: source_syncs fk_source_syncs_source_id_sources; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.source_syncs
    ADD CONSTRAINT fk_source_syncs_source_id_sources FOREIGN KEY (source_id) REFERENCES public.sources(id) ON DELETE RESTRICT;


--
-- Name: sources fk_sources_current_revision; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sources
    ADD CONSTRAINT fk_sources_current_revision FOREIGN KEY (id, current_revision_id) REFERENCES public.source_revisions(source_id, id) DEFERRABLE INITIALLY DEFERRED;


--
-- Name: sources fk_sources_knowledge_base_id_knowledge_bases; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sources
    ADD CONSTRAINT fk_sources_knowledge_base_id_knowledge_bases FOREIGN KEY (knowledge_base_id) REFERENCES public.knowledge_bases(id) ON DELETE RESTRICT;


--
-- Name: website_revision_pages fk_website_revision_pages_reused_revision; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.website_revision_pages
    ADD CONSTRAINT fk_website_revision_pages_reused_revision FOREIGN KEY (source_id, reused_from_revision_id) REFERENCES public.source_revisions(source_id, id) ON DELETE RESTRICT;


--
-- Name: website_revision_pages fk_website_revision_pages_revision; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.website_revision_pages
    ADD CONSTRAINT fk_website_revision_pages_revision FOREIGN KEY (source_id, revision_id) REFERENCES public.source_revisions(source_id, id) ON DELETE CASCADE;


--
-- Name: website_sources fk_website_sources_credential_id_credentials; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.website_sources
    ADD CONSTRAINT fk_website_sources_credential_id_credentials FOREIGN KEY (credential_id) REFERENCES public.credentials(id) ON DELETE RESTRICT;


--
-- Name: website_sources fk_website_sources_source_id_sources; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.website_sources
    ADD CONSTRAINT fk_website_sources_source_id_sources FOREIGN KEY (source_id) REFERENCES public.sources(id) ON DELETE CASCADE;


--
-- Name: website_sources fk_website_sources_tinyfish_credential_id_credentials; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.website_sources
    ADD CONSTRAINT fk_website_sources_tinyfish_credential_id_credentials FOREIGN KEY (tinyfish_credential_id) REFERENCES public.credentials(id) ON DELETE RESTRICT;


--
-- Name: wiki_pages fk_wiki_pages_wiki_version_id_wiki_versions; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wiki_pages
    ADD CONSTRAINT fk_wiki_pages_wiki_version_id_wiki_versions FOREIGN KEY (wiki_version_id) REFERENCES public.wiki_versions(id) ON DELETE CASCADE;


--
-- Name: wiki_versions fk_wiki_versions_documentation_run_id_documentation_runs; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wiki_versions
    ADD CONSTRAINT fk_wiki_versions_documentation_run_id_documentation_runs FOREIGN KEY (documentation_run_id) REFERENCES public.documentation_runs(id) ON DELETE RESTRICT;


--
-- Name: wiki_versions fk_wiki_versions_knowledge_base_id_knowledge_bases; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.wiki_versions
    ADD CONSTRAINT fk_wiki_versions_knowledge_base_id_knowledge_bases FOREIGN KEY (knowledge_base_id) REFERENCES public.knowledge_bases(id) ON DELETE RESTRICT;


--
-- PostgreSQL database dump complete
--

CREATE TABLE public.provider_call_leases (
    id uuid DEFAULT gen_random_uuid() PRIMARY KEY,
    endpoint_id uuid NOT NULL REFERENCES public.provider_endpoints(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);
CREATE INDEX ix_provider_call_leases_endpoint ON public.provider_call_leases(endpoint_id, expires_at);

-- +goose Down

DROP TABLE IF EXISTS
    public.agent_run_knowledge_bases,
    public.agent_run_scope_reservations,
    public.agent_runs,
    public.agent_version_knowledge_bases,
    public.agent_versions,
    public.agents,
    public.audit_events,
    public.artifact_deletion_intents,
    public.bootstrap_tokens,
    public.chat_access_token_agents,
    public.chat_access_tokens,
    public.channel_binding_triggers,
    public.channel_bindings,
    public.claims,
    public.credential_rotation_attempts,
    public.credentials,
    public.discord_channels,
    public.discord_conversation_messages,
    public.discord_conversations,
    public.discord_connections,
    public.discord_roles,
    public.discord_servers,
    public.discovery_runs,
    public.documentation_pages,
    public.documentation_run_models,
    public.documentation_run_sources,
    public.documentation_runs,
    public.event_log,
    public.event_stream_state,
    public.evidence,
    public.idempotency_records,
    public.job_attempts,
    public.job_events,
    public.jobs,
    public.knowledge_bases,
    public.model_assignments,
    public.model_profile_versions,
    public.model_profiles,
    public.operator_sessions,
    public.operators,
    public.probe_runs,
    public.provider_call_leases,
    public.provider_endpoints,
    public.rate_limit_buckets,
    public.repository_sources,
    public.source_revisions,
    public.source_syncs,
    public.sources,
    public.website_revision_pages,
    public.website_sources,
    public.wiki_pages,
    public.wiki_versions
CASCADE;
