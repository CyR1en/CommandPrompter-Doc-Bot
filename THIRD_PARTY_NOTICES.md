# Third-party notices

This notice bundle accompanies the ref0 application and Pi capsule images. It
was reviewed against `go.mod`, `go.sum`, `frontend/package-lock.json`, and
`capsule/package-lock.json` on 2026-09-04. The tables are a curated inventory
of code and font families shipped in the images, not a replacement dependency
lockfile. Exact package versions remain pinned by those lockfiles.

The copyright notices in each inventory and the corresponding license text
below must be read together. The capsule also retains each installed package's
own `package.json`, `LICENSE`, `NOTICE`, or `COPYING` file under
`/opt/capsule/node_modules` when the package publishes one. The capsule's
Node.js distribution retains its upstream license and bundled-dependency
notices at `/usr/local/LICENSE`; Debian package notices remain under
`/usr/share/doc` in both images.

## Statically linked Go binaries

| Component | Version | License | Copyright or required notice |
| --- | --- | --- | --- |
| Go runtime and standard library | 1.27.0 | BSD-3-Clause | Copyright 2009 The Go Authors; linked into the application and capsule supervisor binaries |
| github.com/beorn7/perks | v1.0.1 | MIT | Copyright 2013 Blake Mizerany; see the Prometheus notice below for the fork attribution |
| github.com/bwmarrin/discordgo | v0.29.0 | BSD-3-Clause | Copyright 2015 Bruce Marriner |
| github.com/cespare/xxhash/v2 | v2.3.0 | MIT | Copyright 2016 Caleb Spare |
| github.com/danielgtaylor/huma/v2 | v2.39.1 | MIT | Copyright 2020 Daniel G. Taylor |
| github.com/gorilla/websocket | v1.5.3 | BSD-2-Clause | Copyright 2013 The Gorilla WebSocket Authors |
| github.com/jackc/pgpassfile | v1.0.0 | MIT | Copyright 2019 Jack Christensen |
| github.com/jackc/pgservicefile | v0.0.0-20240606120523-5a60cdf6a761 | MIT | Copyright 2020 Jack Christensen |
| github.com/jackc/pgx/v5 | v5.10.0 | MIT | Copyright 2013-2021 Jack Christensen |
| github.com/jackc/puddle/v2 | v2.2.2 | MIT | Copyright 2018 Jack Christensen |
| github.com/mfridman/interpolate | v0.0.2 | MIT | Copyright 2014-2017 Buildkite Pty Ltd; Copyright 2023 Michael Fridman |
| github.com/munnerz/goautoneg | v0.0.0-20191010083416-a7dc8b61c822 | BSD-3-Clause | Copyright 2011 Open Knowledge Foundation Ltd. |
| github.com/pressly/goose/v3 | v3.27.3 | MIT | Copyright 2012 Liam Staskawicz; 2016 Vojtech Vitek; 2021 Michael Fridman and Vojtech Vitek |
| github.com/prometheus/client_golang | v1.24.1 | Apache-2.0 and bundled BSD-3-Clause code | Prometheus and bundled-code notices reproduced below |
| github.com/prometheus/client_model | v0.6.2 | Apache-2.0 | Prometheus notice reproduced below |
| github.com/prometheus/common | v0.70.1 | Apache-2.0 | Prometheus notice reproduced below |
| github.com/prometheus/procfs | v0.21.1 | Apache-2.0 | Prometheus notice reproduced below |
| github.com/sethvargo/go-retry | v0.4.0 | Apache-2.0 | Upstream Apache-2.0 terms |
| go.uber.org/multierr | v1.11.0 | MIT | Copyright 2017-2021 Uber Technologies, Inc. |
| golang.org/x/crypto | v0.56.0 | BSD-3-Clause | Copyright 2009 The Go Authors |
| golang.org/x/net | v0.57.0 | BSD-3-Clause | Copyright 2009 The Go Authors |
| golang.org/x/sync | v0.22.0 | BSD-3-Clause | Copyright 2009 The Go Authors |
| golang.org/x/sys | v0.47.0 | BSD-3-Clause | Copyright 2009 The Go Authors |
| golang.org/x/text | v0.41.0 | BSD-3-Clause | Copyright 2009 The Go Authors |
| google.golang.org/protobuf | v1.36.11 | BSD-3-Clause | Copyright 2018 The Go Authors |

## Bundled browser application

| Component | Version | License | Copyright or required notice |
| --- | --- | --- | --- |
| @fontsource-variable/jetbrains-mono | 5.3.0 | OFL-1.1 | Copyright 2020 The JetBrains Mono Project Authors; complete font license below |
| @fontsource-variable/outfit | 5.3.0 | OFL-1.1 | Copyright 2021 The Outfit Project Authors; complete font license below |
| @tanstack/history, @tanstack/query-core, @tanstack/react-query, @tanstack/react-router, @tanstack/router-core | 1.162.1, 5.102.8, 5.102.8, 1.170.32, 1.171.27 | MIT | Copyright 2021-present Tanner Linsley |
| @tanstack/react-store, @tanstack/store | 0.9.3 | MIT | Copyright 2021 Tanner Linsley |
| cookie-es | 3.1.1 | MIT | Copyright Pooya Parsa; Roman Shtylman; Douglas Christopher Wilson; Nathan Friedly |
| isbot | 5.2.2 | Unlicense | Public-domain dedication reproduced below |
| openapi-fetch, openapi-typescript-helpers | 0.17.0, 0.1.0 | MIT | Copyright 2023 Drew Powers |
| react, react-dom, scheduler, use-sync-external-store | 19.2.8, 19.2.8, 0.27.0, 1.6.0 | MIT | Copyright Meta Platforms, Inc. and affiliates |
| seroval, seroval-plugins | 1.6.4 | MIT | Copyright 2025 Alexis Munsayac |
| Vite module-preload runtime | 8.2.2 | MIT | Copyright 2019-present VoidZero Inc. and Vite contributors; build-time runtime code is embedded in the browser bundle |
| Rolldown-generated module helpers | 1.2.6 | MIT | Copyright 2024-present VoidZero Inc. & Contributors; build-time helper code is embedded in the browser bundle |

### Markdown renderer and runtime dependencies

The published browser bundle includes these packages. Their notices below use
the complete MIT and ISC license terms reproduced in this document.

| Component | Version | License | Copyright or required notice |
| --- | --- | --- | --- |
| @ungap/structured-clone | 1.4.0 | ISC | Copyright (c) 2021, Andrea Giammarchi, @WebReflection |
| bail | 2.0.2 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| ccount | 2.0.1 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| character-entities | 2.0.2 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| character-entities-html4 | 2.1.0 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| character-entities-legacy | 3.0.0 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| character-reference-invalid | 2.0.1 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| comma-separated-tokens | 2.0.3 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| debug | 4.4.3 | MIT | Copyright (c) 2014-2017 TJ Holowaychuk <tj@vision-media.ca>; Copyright (c) 2018-2021 Josh Junon |
| decode-named-character-reference | 1.3.0 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| dequal | 2.0.3 | MIT | Copyright (c) Luke Edwards <luke.edwards05@gmail.com> (lukeed.com) |
| devlop | 1.1.0 | MIT | Copyright (c) 2023 Titus Wormer <tituswormer@gmail.com> |
| escape-string-regexp | 5.0.0 | MIT | Copyright (c) Sindre Sorhus <sindresorhus@gmail.com> (https://sindresorhus.com) |
| estree-util-is-identifier-name | 3.0.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| extend | 3.0.2 | MIT | Copyright (c) 2014 Stefan Thomas |
| github-slugger | 2.0.0 | ISC | Copyright (c) 2015, Dan Flettre <fletd01@yahoo.com> |
| hast-util-heading-rank | 3.0.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| hast-util-to-jsx-runtime | 2.3.6 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| hast-util-to-string | 3.0.1 | MIT | Copyright (c) Titus Wormer |
| hast-util-whitespace | 3.0.0 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| html-url-attributes | 3.0.1 | MIT | Copyright (c) Titus Wormer |
| inline-style-parser | 0.2.7 | MIT | Copyright (c) 2012 TJ Holowaychuk <tj@vision-media.ca> |
| is-alphabetical | 2.0.1 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| is-alphanumerical | 2.0.1 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| is-decimal | 2.0.1 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| is-hexadecimal | 2.0.1 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| is-plain-obj | 4.1.0 | MIT | Copyright (c) Sindre Sorhus <sindresorhus@gmail.com> (https://sindresorhus.com) |
| longest-streak | 3.1.0 | MIT | Copyright (c) 2015 Titus Wormer <mailto:tituswormer@gmail.com> |
| markdown-table | 3.0.4 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| mdast-util-find-and-replace | 3.0.2 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| mdast-util-from-markdown | 2.0.3 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| mdast-util-gfm | 3.1.0 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| mdast-util-gfm-autolink-literal | 2.0.1 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| mdast-util-gfm-footnote | 2.1.0 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| mdast-util-gfm-strikethrough | 2.0.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| mdast-util-gfm-table | 2.0.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| mdast-util-gfm-task-list-item | 2.0.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| mdast-util-mdx-expression | 2.0.1 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| mdast-util-mdx-jsx | 3.2.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| mdast-util-mdxjs-esm | 2.0.1 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| mdast-util-phrasing | 4.1.0 | MIT | Copyright (c) 2017 Titus Wormer <tituswormer@gmail.com>; Copyright (c) 2017 Victor Felder <victor@draft.li> |
| mdast-util-to-hast | 13.2.1 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| mdast-util-to-markdown | 2.1.2 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| mdast-util-to-string | 4.0.0 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| micromark | 4.0.2 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-core-commonmark | 2.0.3 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-extension-gfm | 3.0.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| micromark-extension-gfm-autolink-literal | 2.1.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| micromark-extension-gfm-footnote | 2.1.0 | MIT | Copyright (c) 2021 Titus Wormer <tituswormer@gmail.com> |
| micromark-extension-gfm-strikethrough | 2.1.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| micromark-extension-gfm-table | 2.1.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-extension-gfm-tagfilter | 2.0.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| micromark-extension-gfm-task-list-item | 2.1.0 | MIT | Copyright (c) 2020 Titus Wormer <tituswormer@gmail.com> |
| micromark-factory-destination | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-factory-label | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-factory-space | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-factory-title | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-factory-whitespace | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-character | 2.1.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-chunked | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-classify-character | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-combine-extensions | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-decode-numeric-character-reference | 2.0.2 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-decode-string | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-encode | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-html-tag-name | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-normalize-identifier | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-resolve-all | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-sanitize-uri | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-subtokenize | 2.1.0 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-symbol | 2.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| micromark-util-types | 2.0.2 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| ms | 2.1.3 | MIT | Copyright (c) 2020 Vercel, Inc. |
| parse-entities | 4.0.2 | MIT | Copyright (c) Titus Wormer <mailto:tituswormer@gmail.com> |
| property-information | 7.2.0 | MIT | Copyright (c) Titus Wormer <mailto:tituswormer@gmail.com> |
| react-markdown | 10.1.0 | MIT | Copyright (c) Espen Hovlandsdal |
| rehype-slug | 6.0.0 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| remark-gfm | 4.0.1 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| remark-parse | 11.0.0 | MIT | Copyright (c) 2014 Titus Wormer <tituswormer@gmail.com> |
| remark-rehype | 11.1.2 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| remark-stringify | 11.0.0 | MIT | Copyright (c) 2014 Titus Wormer <tituswormer@gmail.com> |
| space-separated-tokens | 2.0.2 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| stringify-entities | 4.0.4 | MIT | Copyright (c) 2015 Titus Wormer <mailto:tituswormer@gmail.com> |
| style-to-js | 1.1.21 | MIT | Copyright (c) 2020 Menglin "Mark" Xu <mark@remarkablemark.org> |
| style-to-object | 1.0.14 | MIT | Copyright (c) 2017 Menglin "Mark" Xu <mark@remarkablemark.org> |
| trim-lines | 3.0.1 | MIT | Copyright (c) 2015 Titus Wormer <mailto:tituswormer@gmail.com> |
| trough | 2.2.0 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| unified | 11.0.5 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| unist-util-is | 6.0.1 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| unist-util-position | 5.0.0 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| unist-util-stringify-position | 4.0.0 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| unist-util-visit | 5.1.0 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| unist-util-visit-parents | 6.0.2 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |
| vfile | 6.0.3 | MIT | Copyright (c) 2015 Titus Wormer <tituswormer@gmail.com> |
| vfile-message | 4.0.3 | MIT | Copyright (c) Titus Wormer <tituswormer@gmail.com> |
| zwitch | 2.0.4 | MIT | Copyright (c) 2016 Titus Wormer <tituswormer@gmail.com> |

## Pi capsule runtime

The capsule's installed production packages are copied intact, including their
published license files. This compact inventory calls out the top-level and
license-family boundaries, plus packages whose npm archives omit a license
file. The lockfile remains the exact transitive inventory.

The archives without their own license file are
`@aws-sdk/credential-provider-http` 3.972.72,
`@aws-sdk/credential-provider-login` 3.972.77,
`@aws-sdk/nested-clients` 3.997.44, `@earendil-works/pi-agent-core` 0.84.4,
`@earendil-works/pi-ai` 0.84.4, `@earendil-works/pi-telemetry` 0.84.4,
and `data-uri-to-buffer` 4.0.1. Their applicable copyright notices and
complete MIT or Apache-2.0 terms are included here.

| Component family | Version or scope | License | Copyright or required notice |
| --- | --- | --- | --- |
| @earendil-works/pi-agent-core, @earendil-works/pi-ai, @earendil-works/pi-telemetry | 0.84.4 | MIT | Copyright (c) 2025 Mario Zechner; these npm archives omit the repository license, so it is reproduced through the MIT terms below |
| typebox | 1.3.7 | MIT | Original license retained in `node_modules` |
| @anthropic-ai/sdk | 0.91.1 | MIT | Copyright 2023 Anthropic, PBC; original license retained |
| @aws-sdk/*, @aws-crypto/*, @aws/lambda-invoke-store, @smithy/* | lockfile versions | Apache-2.0 | AWS SDK for JavaScript Team; the complete Apache-2.0 terms below also cover internal AWS packages whose npm archives omit a license file |
| @google/genai, google-auth-library, gaxios, gcp-metadata, google-logging-utils | lockfile versions | Apache-2.0 | Original license files retained |
| openai | 6.40.0 | Apache-2.0 | Original license retained |
| @protobufjs/*, protobufjs, diff, buffer-equal-constant-time | lockfile versions | BSD-3-Clause | Original license files retained |
| tslib | 2.8.1 | 0BSD | Original license retained |
| yaml | 2.9.0 | ISC | Original license retained |
| data-uri-to-buffer | 4.0.1 | MIT | Nathan Rajlich; its npm archive omits a license file, so the MIT terms below apply |
| Remaining support packages | lockfile versions | MIT, BSD-3-Clause, Apache-2.0 | Original license files and package metadata retained in `node_modules` |

## MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## BSD 2-Clause License

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
   this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.

## BSD 3-Clause License

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice,
   this list of conditions and the following disclaimer.
2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.
3. Neither the name of the copyright holder nor the names of its contributors
   may be used to endorse or promote products derived from this software
   without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
POSSIBILITY OF SUCH DAMAGE.

## Apache License

                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

   1. Definitions.

      "License" shall mean the terms and conditions for use, reproduction,
      and distribution as defined by Sections 1 through 9 of this document.

      "Licensor" shall mean the copyright owner or entity authorized by
      the copyright owner that is granting the License.

      "Legal Entity" shall mean the union of the acting entity and all
      other entities that control, are controlled by, or are under common
      control with that entity. For the purposes of this definition,
      "control" means (i) the power, direct or indirect, to cause the
      direction or management of such entity, whether by contract or
      otherwise, or (ii) ownership of fifty percent (50%) or more of the
      outstanding shares, or (iii) beneficial ownership of such entity.

      "You" (or "Your") shall mean an individual or Legal Entity
      exercising permissions granted by this License.

      "Source" form shall mean the preferred form for making modifications,
      including but not limited to software source code, documentation
      source, and configuration files.

      "Object" form shall mean any form resulting from mechanical
      transformation or translation of a Source form, including but
      not limited to compiled object code, generated documentation,
      and conversions to other media types.

      "Work" shall mean the work of authorship, whether in Source or
      Object form, made available under the License, as indicated by a
      copyright notice that is included in or attached to the work
      (an example is provided in the Appendix below).

      "Derivative Works" shall mean any work, whether in Source or Object
      form, that is based on (or derived from) the Work and for which the
      editorial revisions, annotations, elaborations, or other modifications
      represent, as a whole, an original work of authorship. For the purposes
      of this License, Derivative Works shall not include works that remain
      separable from, or merely link (or bind by name) to the interfaces of,
      the Work and Derivative Works thereof.

      "Contribution" shall mean any work of authorship, including
      the original version of the Work and any modifications or additions
      to that Work or Derivative Works thereof, that is intentionally
      submitted to Licensor for inclusion in the Work by the copyright owner
      or by an individual or Legal Entity authorized to submit on behalf of
      the copyright owner. For the purposes of this definition, "submitted"
      means any form of electronic, verbal, or written communication sent
      to the Licensor or its representatives, including but not limited to
      communication on electronic mailing lists, source code control systems,
      and issue tracking systems that are managed by, or on behalf of, the
      Licensor for the purpose of discussing and improving the Work, but
      excluding communication that is conspicuously marked or otherwise
      designated in writing by the copyright owner as "Not a Contribution."

      "Contributor" shall mean Licensor and any individual or Legal Entity
      on behalf of whom a Contribution has been received by Licensor and
      subsequently incorporated within the Work.

   2. Grant of Copyright License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      copyright license to reproduce, prepare Derivative Works of,
      publicly display, publicly perform, sublicense, and distribute the
      Work and such Derivative Works in Source or Object form.

   3. Grant of Patent License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      (except as stated in this section) patent license to make, have made,
      use, offer to sell, sell, import, and otherwise transfer the Work,
      where such license applies only to those patent claims licensable
      by such Contributor that are necessarily infringed by their
      Contribution(s) alone or by combination of their Contribution(s)
      with the Work to which such Contribution(s) was submitted. If You
      institute patent litigation against any entity (including a
      cross-claim or counterclaim in a lawsuit) alleging that the Work
      or a Contribution incorporated within the Work constitutes direct
      or contributory patent infringement, then any patent licenses
      granted to You under this License for that Work shall terminate
      as of the date such litigation is filed.

   4. Redistribution. You may reproduce and distribute copies of the
      Work or Derivative Works thereof in any medium, with or without
      modifications, and in Source or Object form, provided that You
      meet the following conditions:

      (a) You must give any other recipients of the Work or
          Derivative Works a copy of this License; and

      (b) You must cause any modified files to carry prominent notices
          stating that You changed the files; and

      (c) You must retain, in the Source form of any Derivative Works
          that You distribute, all copyright, patent, trademark, and
          attribution notices from the Source form of the Work,
          excluding those notices that do not pertain to any part of
          the Derivative Works; and

      (d) If the Work includes a "NOTICE" text file as part of its
          distribution, then any Derivative Works that You distribute must
          include a readable copy of the attribution notices contained
          within such NOTICE file, excluding those notices that do not
          pertain to any part of the Derivative Works, in at least one
          of the following places: within a NOTICE text file distributed
          as part of the Derivative Works; within the Source form or
          documentation, if provided along with the Derivative Works; or,
          within a display generated by the Derivative Works, if and
          wherever such third-party notices normally appear. The contents
          of the NOTICE file are for informational purposes only and
          do not modify the License. You may add Your own attribution
          notices within Derivative Works that You distribute, alongside
          or as an addendum to the NOTICE text from the Work, provided
          that such additional attribution notices cannot be construed
          as modifying the License.

      You may add Your own copyright statement to Your modifications and
      may provide additional or different license terms and conditions
      for use, reproduction, or distribution of Your modifications, or
      for any such Derivative Works as a whole, provided Your use,
      reproduction, and distribution of the Work otherwise complies with
      the conditions stated in this License.

   5. Submission of Contributions. Unless You explicitly state otherwise,
      any Contribution intentionally submitted for inclusion in the Work
      by You to the Licensor shall be under the terms and conditions of
      this License, without any additional terms or conditions.
      Notwithstanding the above, nothing herein shall supersede or modify
      the terms of any separate license agreement you may have executed
      with Licensor regarding such Contributions.

   6. Trademarks. This License does not grant permission to use the trade
      names, trademarks, service marks, or product names of the Licensor,
      except as required for reasonable and customary use in describing the
      origin of the Work and reproducing the content of the NOTICE file.

   7. Disclaimer of Warranty. Unless required by applicable law or
      agreed to in writing, Licensor provides the Work (and each
      Contributor provides its Contributions) on an "AS IS" BASIS,
      WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
      implied, including, without limitation, any warranties or conditions
      of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A
      PARTICULAR PURPOSE. You are solely responsible for determining the
      appropriateness of using or redistributing the Work and assume any
      risks associated with Your exercise of permissions under this License.

   8. Limitation of Liability. In no event and under no legal theory,
      whether in tort (including negligence), contract, or otherwise,
      unless required by applicable law (such as deliberate and grossly
      negligent acts) or agreed to in writing, shall any Contributor be
      liable to You for damages, including any direct, indirect, special,
      incidental, or consequential damages of any character arising as a
      result of this License or out of the use or inability to use the
      Work (including but not limited to damages for loss of goodwill,
      work stoppage, computer failure or malfunction, or any and all
      other commercial damages or losses), even if such Contributor
      has been advised of the possibility of such damages.

   9. Accepting Warranty or Additional Liability. While redistributing
      the Work or Derivative Works thereof, You may choose to offer,
      and charge a fee for, acceptance of support, warranty, indemnity,
      or other liability obligations and/or rights consistent with this
      License. However, in accepting such obligations, You may act only
      on Your own behalf and on Your sole responsibility, not on behalf
      of any other Contributor, and only if You agree to indemnify,
      defend, and hold each Contributor harmless for any liability
      incurred by, or claims asserted against, such Contributor by reason
      of your accepting any such warranty or additional liability.

   END OF TERMS AND CONDITIONS

   APPENDIX: How to apply the Apache License to your work.

      To apply the Apache License to your work, attach the following
      boilerplate notice, with the fields enclosed by brackets "[]"
      replaced with your own identifying information. (Don't include
      the brackets!) The text should be enclosed in the appropriate
      comment syntax for the file format. We also recommend that a file
      or class name and description of purpose be included on the same
      "printed page" as the copyright notice for easier identification
      within third-party archives.

   Copyright [yyyy] [name of copyright owner]

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.

### Prometheus NOTICE text

Prometheus instrumentation library for Go applications
Copyright 2012-2015 The Prometheus Authors

This product includes software developed at SoundCloud Ltd.
(http://soundcloud.com/).

The following components are included in this product:

perks - a fork of https://github.com/bmizerany/perks
https://github.com/beorn7/perks
Copyright 2013-2015 Blake Mizerany, Björn Rabenstein
See https://github.com/beorn7/perks/blob/master/README.md for license details.

Go support for Protocol Buffers - Google's data interchange format
http://github.com/golang/protobuf/
Copyright 2010 The Go Authors
See source code for license details.

Data model artifacts for Prometheus.
Copyright 2012-2015 The Prometheus Authors

Common libraries shared by Prometheus Go components.
Copyright 2015 The Prometheus Authors

procfs provides functions to retrieve system, kernel and process metrics from
the pseudo-filesystem proc.
Copyright 2014-2015 The Prometheus Authors

## ISC License

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH
REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY
AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT,
INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES WHATSOEVER RESULTING FROM
LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR
OTHER TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR
PERFORMANCE OF THIS SOFTWARE.

## Zero-Clause BSD License (0BSD)

Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH
REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY
AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT,
INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES WHATSOEVER RESULTING FROM
LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR
OTHER TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR
PERFORMANCE OF THIS SOFTWARE.

## Unlicense

This is free and unencumbered software released into the public domain.

Anyone is free to copy, modify, publish, use, compile, sell, or distribute this
software, either in source code form or as a compiled binary, for any purpose,
commercial or non-commercial, and by any means.

In jurisdictions that recognize copyright laws, the author or authors of this
software dedicate any and all copyright interest in the software to the public
domain. We make this dedication for the benefit of the public at large and to
the detriment of our heirs and successors. We intend this dedication to be an
overt act of relinquishment in perpetuity of all present and future rights to
this software under copyright law.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN
ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

For more information, please refer to <http://unlicense.org/>.

## JetBrains Mono font license

Copyright 2020 The JetBrains Mono Project Authors
(https://github.com/JetBrains/JetBrainsMono)

JetBrainsMono-Italic[wght].ttf: Copyright 2020 The JetBrains Mono Project
Authors (https://github.com/JetBrains/JetBrainsMono)

This Font Software is licensed under the SIL Open Font License, Version 1.1.

-----------------------------------------------------------
SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007
-----------------------------------------------------------

PREAMBLE
The goals of the Open Font License (OFL) are to stimulate worldwide
development of collaborative font projects, to support the font creation
efforts of academic and linguistic communities, and to provide a free and
open framework in which fonts may be shared and improved in partnership
with others.

The OFL allows the licensed fonts to be used, studied, modified and
redistributed freely as long as they are not sold by themselves. The fonts,
including any derivative works, can be bundled, embedded, redistributed
and/or sold with any software provided that any reserved names are not used
by derivative works. The fonts and derivatives, however, cannot be released
under any other type of license. The requirement for fonts to remain under
this license does not apply to any document created using the fonts or their
derivatives.

DEFINITIONS
"Font Software" refers to the set of files released by the Copyright
Holder(s) under this license and clearly marked as such. This may include
source files, build scripts and documentation.

"Reserved Font Name" refers to any names specified as such after the
copyright statement(s).

"Original Version" refers to the collection of Font Software components as
distributed by the Copyright Holder(s).

"Modified Version" refers to any derivative made by adding to, deleting, or
substituting -- in part or in whole -- any of the components of the Original
Version, by changing formats or by porting the Font Software to a new
environment.

"Author" refers to any designer, engineer, programmer, technical writer or
other person who contributed to the Font Software.

PERMISSION & CONDITIONS
Permission is hereby granted, free of charge, to any person obtaining a copy
of the Font Software, to use, study, copy, merge, embed, modify, redistribute,
and sell modified and unmodified copies of the Font Software, subject to the
following conditions:

1) Neither the Font Software nor any of its individual components, in Original
or Modified Versions, may be sold by itself.

2) Original or Modified Versions of the Font Software may be bundled,
redistributed and/or sold with any software, provided that each copy contains
the above copyright notice and this license. These can be included either as
stand-alone text files, human-readable headers or in the appropriate
machine-readable metadata fields within text or binary files as long as those
fields can be easily viewed by the user.

3) No Modified Version of the Font Software may use the Reserved Font Name(s)
unless explicit written permission is granted by the corresponding Copyright
Holder. This restriction only applies to the primary font name as presented to
the users.

4) The name(s) of the Copyright Holder(s) or the Author(s) of the Font Software
shall not be used to promote, endorse or advertise any Modified Version,
except to acknowledge the contribution(s) of the Copyright Holder(s) and the
Author(s) or with their explicit written permission.

5) The Font Software, modified or unmodified, in part or in whole, must be
distributed entirely under this license, and must not be distributed under any
other license. The requirement for fonts to remain under this license does not
apply to any document created using the Font Software.

TERMINATION
This license becomes null and void if any of the above conditions are not met.

DISCLAIMER
THE FONT SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS
OR IMPLIED, INCLUDING BUT NOT LIMITED TO ANY WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT OF COPYRIGHT, PATENT,
TRADEMARK, OR OTHER RIGHT. IN NO EVENT SHALL THE COPYRIGHT HOLDER BE LIABLE FOR
ANY CLAIM, DAMAGES OR OTHER LIABILITY, INCLUDING ANY GENERAL, SPECIAL,
INDIRECT, INCIDENTAL, OR CONSEQUENTIAL DAMAGES, WHETHER IN AN ACTION OF
CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF THE USE OR INABILITY TO USE
THE FONT SOFTWARE OR FROM OTHER DEALINGS IN THE FONT SOFTWARE.

## Outfit font license

Copyright 2021 The Outfit Project Authors
(https://github.com/Outfitio/Outfit-Fonts)

This Font Software is licensed under the SIL Open Font License, Version 1.1.

-----------------------------------------------------------
SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007
-----------------------------------------------------------

PREAMBLE
The goals of the Open Font License (OFL) are to stimulate worldwide
development of collaborative font projects, to support the font creation
efforts of academic and linguistic communities, and to provide a free and
open framework in which fonts may be shared and improved in partnership
with others.

The OFL allows the licensed fonts to be used, studied, modified and
redistributed freely as long as they are not sold by themselves. The fonts,
including any derivative works, can be bundled, embedded, redistributed
and/or sold with any software provided that any reserved names are not used
by derivative works. The fonts and derivatives, however, cannot be released
under any other type of license. The requirement for fonts to remain under
this license does not apply to any document created using the fonts or their
derivatives.

DEFINITIONS
"Font Software" refers to the set of files released by the Copyright
Holder(s) under this license and clearly marked as such. This may include
source files, build scripts and documentation.

"Reserved Font Name" refers to any names specified as such after the
copyright statement(s).

"Original Version" refers to the collection of Font Software components as
distributed by the Copyright Holder(s).

"Modified Version" refers to any derivative made by adding to, deleting, or
substituting -- in part or in whole -- any of the components of the Original
Version, by changing formats or by porting the Font Software to a new
environment.

"Author" refers to any designer, engineer, programmer, technical writer or
other person who contributed to the Font Software.

PERMISSION & CONDITIONS
Permission is hereby granted, free of charge, to any person obtaining a copy
of the Font Software, to use, study, copy, merge, embed, modify, redistribute,
and sell modified and unmodified copies of the Font Software, subject to the
following conditions:

1) Neither the Font Software nor any of its individual components, in Original
or Modified Versions, may be sold by itself.

2) Original or Modified Versions of the Font Software may be bundled,
redistributed and/or sold with any software, provided that each copy contains
the above copyright notice and this license. These can be included either as
stand-alone text files, human-readable headers or in the appropriate
machine-readable metadata fields within text or binary files as long as those
fields can be easily viewed by the user.

3) No Modified Version of the Font Software may use the Reserved Font Name(s)
unless explicit written permission is granted by the corresponding Copyright
Holder. This restriction only applies to the primary font name as presented to
the users.

4) The name(s) of the Copyright Holder(s) or the Author(s) of the Font Software
shall not be used to promote, endorse or advertise any Modified Version,
except to acknowledge the contribution(s) of the Copyright Holder(s) and the
Author(s) or with their explicit written permission.

5) The Font Software, modified or unmodified, in part or in whole, must be
distributed entirely under this license, and must not be distributed under any
other license. The requirement for fonts to remain under this license does not
apply to any document created using the Font Software.

TERMINATION
This license becomes null and void if any of the above conditions are not met.

DISCLAIMER
THE FONT SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS
OR IMPLIED, INCLUDING BUT NOT LIMITED TO ANY WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT OF COPYRIGHT, PATENT,
TRADEMARK, OR OTHER RIGHT. IN NO EVENT SHALL THE COPYRIGHT HOLDER BE LIABLE FOR
ANY CLAIM, DAMAGES OR OTHER LIABILITY, INCLUDING ANY GENERAL, SPECIAL,
INDIRECT, INCIDENTAL, OR CONSEQUENTIAL DAMAGES, WHETHER IN AN ACTION OF
CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF THE USE OR INABILITY TO USE
THE FONT SOFTWARE OR FROM OTHER DEALINGS IN THE FONT SOFTWARE.
