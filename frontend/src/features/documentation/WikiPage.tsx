import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useEffect, useState, type ReactNode } from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";
import rehypeSlug from "rehype-slug";

import { getWiki, getWikiEvidence, listKnowledgeBases, listWikiVersions, safeErrorMessage, wikiExportUrl, type WikiEvidence } from "../../api/client";
import { queryKeys } from "../../api/queries";
import { Select } from "../../app/Select";
import { EmptyState, ErrorNotice } from "../../app/StatusBadge";

export type WikiSearch = { knowledge_base_id?: string; version_id?: string; slug?: string };

export function parseWikiSearch(value: Record<string, unknown>): WikiSearch {
  const uuid = (input: unknown): string | undefined => typeof input === "string" && /^[a-f0-9-]{36}$/i.test(input) ? input : undefined;
  return { knowledge_base_id: uuid(value.knowledge_base_id), version_id: uuid(value.version_id), slug: typeof value.slug === "string" && /^[a-z0-9-]+(?:\/[a-z0-9-]+)*$/.test(value.slug) && value.slug.length <= 255 ? value.slug : undefined };
}

export function WikiPage({ search = {} }: { search?: WikiSearch }): ReactNode {
  const knowledgeBases = useQuery({ queryKey: queryKeys.knowledgeBases, queryFn: listKnowledgeBases });
  const navigate = useNavigate({ from: "/wiki" });
  const knowledgeBaseId = search.knowledge_base_id ?? knowledgeBases.data?.[0]?.id ?? "";
  const versionId = search.version_id ?? "";
  const slug = search.slug ?? "";

  useEffect(() => {
    if (!search.knowledge_base_id && knowledgeBaseId) void navigate({ search: { knowledge_base_id: knowledgeBaseId }, replace: true });
  }, [search.knowledge_base_id, knowledgeBaseId, navigate]);

  const versions = useQuery({
    enabled: knowledgeBaseId !== "",
    queryKey: [...queryKeys.wiki, knowledgeBaseId, "versions"],
    queryFn: () => listWikiVersions(knowledgeBaseId),
  });
  const wiki = useQuery({
    enabled: knowledgeBaseId !== "" && (versions.data?.length ?? 0) > 0,
    queryKey: [...queryKeys.wiki, knowledgeBaseId, versionId || "current", slug || "first"],
    queryFn: () => getWiki(knowledgeBaseId, { slug: slug || undefined, versionId: versionId || undefined }),
  });
  useEffect(() => {
    const first = wiki.data?.pages[0];
    if (!slug && first) void navigate({ search: { knowledge_base_id: knowledgeBaseId, version_id: versionId || undefined, slug: first.slug }, replace: true });
  }, [slug, wiki.data, knowledgeBaseId, versionId, navigate]);
  const selectedKnowledgeBase = knowledgeBases.data?.find((item) => item.id === knowledgeBaseId);
  const page = wiki.data?.page;

  function selectKnowledgeBase(value: string): void {
    void navigate({ search: { knowledge_base_id: value } });
  }

  function markdownLink(href: string | undefined, children: ReactNode): ReactNode {
    if (!href) return <span>{children}</span>;
    if (/^https?:\/\//i.test(href)) return <a href={href} rel="noreferrer noopener">{children}</a>;
    if (href.startsWith("//") || /^[a-z][a-z0-9+.-]*:/i.test(href)) return <span>{children}</span>;
    const target = new URL(href, `https://wiki.invalid/${page?.slug ?? "overview"}.md`);
    let targetSlug: string;
    let fragment: string;
    try {
      targetSlug = decodeURIComponent(target.pathname.slice(1)).replace(/\.md$/, "");
      fragment = decodeURIComponent(target.hash.slice(1));
    } catch { return <span>{children}</span>; }
    const hash = fragment && !fragment.startsWith("user-content-") ? `wiki-${fragment}` : fragment;
    if (!wiki.data?.pages.some((item) => item.slug === targetSlug)) return <span>{children}</span>;
    return <Link to="/wiki" search={{ knowledge_base_id: knowledgeBaseId, version_id: versionId || undefined, slug: targetSlug }} hash={hash}>{children}</Link>;
  }

  return (
    <section className="page wiki-page">
      <header className="page-heading">
        <div><p className="eyebrow">published wiki</p><h1>Linked knowledge reader</h1></div>
        <div className="wiki-selectors">
          <label>
            Knowledge base
            <Select
              onChange={selectKnowledgeBase}
              options={[{ label: "Select a knowledge base", value: "" }, ...(knowledgeBases.data ?? []).map((item) => ({ label: item.name, value: item.id }))]}
              value={knowledgeBaseId}
            />
          </label>
          <label>
            Published version
            <Select
              disabled={(versions.data?.length ?? 0) === 0}
              onChange={(next) => { void navigate({ search: { knowledge_base_id: knowledgeBaseId, version_id: next || undefined } }); }}
              options={[{ label: "Current publication", value: "" }, ...(versions.data ?? []).map((version) => ({ label: `${formatDate(version.published_at)} · ${version.page_count} pages`, value: version.id }))]}
              value={versionId}
            />
          </label>
        </div>
      </header>
      <ErrorNotice message={knowledgeBases.error ? safeErrorMessage(knowledgeBases.error) : versions.error ? safeErrorMessage(versions.error) : wiki.error ? safeErrorMessage(wiki.error) : null} />
      {knowledgeBases.isPending || versions.isLoading || wiki.isLoading ? <p aria-live="polite" className="notice">Loading published wiki…</p> : null}
      {knowledgeBases.data?.length === 0 ? <EmptyState>Create a knowledge base before opening the wiki reader.</EmptyState> : null}
      {knowledgeBaseId && versions.data?.length === 0 ? <EmptyState>{selectedKnowledgeBase?.name ?? "This knowledge base"} has no published wiki version.</EmptyState> : null}
      {wiki.data ? (
        <div className="wiki-reader">
          <aside aria-labelledby="wiki-pages-title" className="wiki-navigation">
            <div className="section-heading"><h2 id="wiki-pages-title">Pages</h2><span>{wiki.data.pages.length}</span></div>
            <nav aria-label="Wiki pages">
              <ol>
                {wiki.data.pages.map((item) => (
                  <li key={item.slug}>
                    <Link aria-current={page?.slug === item.slug ? "page" : undefined} to="/wiki" search={{ knowledge_base_id: knowledgeBaseId, version_id: versionId || undefined, slug: item.slug }}>
                      <strong>{item.title}</strong><span>{item.page_type} · {item.slug}</span>
                    </Link>
                  </li>
                ))}
              </ol>
            </nav>
            <a className="button secondary wiki-export" download href={wikiExportUrl(knowledgeBaseId, versionId || undefined)}>Export Markdown bundle</a>
          </aside>
          <article className="wiki-document">
            <header>
              <p className="eyebrow">{page?.page_type ?? "Published page"}</p>
              <h2>{page?.title ?? "No page selected"}</h2>
              {page ? <p className="lede">{page.description}</p> : null}
            </header>
            {page ? (
              <>
                <section aria-labelledby="wiki-content-title" className="wiki-content">
                  <h3 className="sr-only" id="wiki-content-title">Page Markdown</h3>
                  <Markdown skipHtml remarkPlugins={[remarkGfm]} rehypePlugins={[[rehypeSlug, { prefix: "wiki-" }]]} components={{
                    a: ({ href, children }) => markdownLink(href, children),
                    img: ({ alt }) => <span>{alt}</span>,
                  }}>{page.markdown.replace(/^---\r?\n[\s\S]*?\r?\n---(?:\r?\n|$)/, "")}</Markdown>
                </section>
                <section aria-labelledby="wiki-provenance-title" className="folio-panel detail-ledger wiki-provenance">
                  <div className="section-heading"><h3 id="wiki-provenance-title">Generation provenance</h3><span>verified publication</span></div>
                  <dl>
                    <div><dt>Documentation run</dt><dd><Link params={{ runId: wiki.data.version.documentation_run_id }} to="/runs/$runId">{wiki.data.version.documentation_run_id}</Link></dd></div>
                    <div><dt>Published</dt><dd>{formatDate(wiki.data.version.published_at)}</dd></div>
                    <div><dt>Content digest</dt><dd><code>{page.content_sha256}</code></dd></div>
                    <div><dt>Claims digest</dt><dd><code>{page.claims_sha256}</code></dd></div>
                    <div><dt>Manifest digest</dt><dd><code>{wiki.data.version.manifest_sha256}</code></dd></div>
                  </dl>
                </section>
                <section aria-labelledby="wiki-claims-title" className="ledger-section">
                  <div className="section-heading"><h3 id="wiki-claims-title">Claims and evidence</h3><span>{page.claims.length} Claims</span></div>
                  {page.claims.map((claim) => (
                    <details className="claim-card" id={`wiki-claim-${claim.id}`} key={claim.id} open>
                      <summary><code>{claim.id}</code> · {claim.statement}</summary>
                      <ol>
                        {claim.evidence.map((evidence) => <li key={evidence.id}><Evidence evidence={evidence} claimId={claim.id} knowledgeBaseId={knowledgeBaseId} versionId={wiki.data.version.id} slug={page.slug} /></li>)}
                      </ol>
                    </details>
                  ))}
                  {page.claims.length === 0 ? <EmptyState>This page contains no source-backed Claims.</EmptyState> : null}
                </section>
              </>
            ) : <EmptyState>This wiki version has no readable page.</EmptyState>}
          </article>
        </div>
      ) : null}
    </section>
  );
}

function Evidence({ evidence, knowledgeBaseId, versionId, slug, claimId }: { claimId: string; evidence: WikiEvidence; knowledgeBaseId: string; versionId: string; slug: string }): ReactNode {
  const [expanded, setExpanded] = useState(false);
  const excerpt = useQuery({ enabled: expanded, queryKey: [...queryKeys.wiki, knowledgeBaseId, versionId, slug, claimId, evidence.id, "evidence"], queryFn: () => getWikiEvidence(knowledgeBaseId, { versionId, slug, claimId, evidenceId: evidence.id }) });
  const lines = evidence.start_line === null ? "whole file" : `lines ${evidence.start_line}–${evidence.end_line}`;
  return (
    <div className="evidence-card">
      <strong>{evidence.path}</strong><span>{lines}</span>
      <span>source {shortId(evidence.source_id)} · revision {shortId(evidence.source_revision_id)} · commit {shortId(evidence.commit)}</span>
      <code>{evidence.resource}</code>
      <button aria-expanded={expanded} className="button secondary" onClick={() => setExpanded(!expanded)} type="button">{expanded ? "Hide" : "Read"} evidence: {evidence.path}</button>
      {expanded && excerpt.isPending ? <p aria-live="polite">Loading evidence…</p> : null}
      {expanded && excerpt.error ? <ErrorNotice message={safeErrorMessage(excerpt.error)} /> : null}
      {expanded && excerpt.data ? <div><p>Lines {excerpt.data.start_line}–{excerpt.data.end_line}{excerpt.data.truncated ? " (excerpt truncated)" : ""}</p><pre>{excerpt.data.text}</pre></div> : null}
    </div>
  );
}

function shortId(value: string): string {
  return value.slice(0, 12);
}

function formatDate(value: string): string {
  return new Date(value).toLocaleString();
}
