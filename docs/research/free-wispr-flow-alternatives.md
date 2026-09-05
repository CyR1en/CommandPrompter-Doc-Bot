# Free Wispr Flow alternatives for macOS

Researched on 2026-08-29. The product is **Wispr Flow**, despite the common
"Whisper Flow" spelling. This note uses current first-party product pages,
documentation, and official source repositories.

## Short answer

Try **OpenWhispr** first. Its signed Mac app gives you a global hotkey,
automatic paste, offline Whisper or Parakeet transcription, and local AI
processing. Local dictation and local AI models are unlimited on the free plan.
Only its hosted cloud transcription has a free-tier cap of 2,000 words per
rolling seven days. [OpenWhispr pricing](https://openwhispr.com/pricing),
[macOS setup](https://docs.openwhispr.com/platform/macos),
[local cleanup](https://docs.openwhispr.com/help/dictation/cleanup),
[MIT-licensed source](https://github.com/OpenWhispr/openwhispr/blob/db3f8c930b260afc4b37898a3940fad5df447f63/README.md#L109-L120).

If your priority is Wispr-style cleanup without any cloud account, try
**VoiceScribe** on an Apple Silicon Mac. Its signed app runs both transcription
and optional LLM cleanup locally. [VoiceScribe pipeline and installation](https://github.com/eddmann/VoiceScribe/blob/d1e29a10d52913c67458d695d417517d17162d9a/README.md#L7-L27),
[requirements and privacy](https://github.com/eddmann/VoiceScribe/blob/d1e29a10d52913c67458d695d417517d17162d9a/README.md#L90-L125).

## Comparison

| Product | Free status and platforms | System-wide dictation | Local processing and cleanup | Main catch |
| --- | --- | --- | --- | --- |
| **OpenWhispr** | Free plan plus paid cloud plans. The desktop source is MIT licensed. macOS 12+, Windows, and Linux are supported, including Intel and Apple Silicon Macs. [License and downloads](https://github.com/OpenWhispr/openwhispr/blob/db3f8c930b260afc4b37898a3940fad5df447f63/README.md#L30-L41), [Mac requirements](https://docs.openwhispr.com/platform/macos) | A global hotkey dictates into any app and pastes automatically. Without Accessibility permission, the text still reaches the clipboard. [Behavior](https://github.com/OpenWhispr/openwhispr/blob/db3f8c930b260afc4b37898a3940fad5df447f63/README.md#L43-L54), [permission behavior](https://docs.openwhispr.com/platform/macos) | Whisper, Parakeet, and the cleanup model can run offline. Cleanup removes fillers and fixes grammar and punctuation. Local use has no limit. [Cleanup behavior and models](https://docs.openwhispr.com/help/dictation/cleanup), [free-plan limits](https://docs.openwhispr.com/faq) | OpenWhispr Cloud is capped at 2,000 words per rolling seven days. BYOK cloud transcription is uncapped by OpenWhispr, but the provider can charge. [Pricing](https://openwhispr.com/pricing) |
| **Handy** | Fully free, MIT licensed, and available for macOS, Windows, and Linux. Prebuilt releases are available. [Project and platforms](https://github.com/cjpais/Handy/blob/6fa850612e1297dd93ddaa64499454030bba2340/README.md#L5-L46), [license](https://github.com/cjpais/Handy/blob/6fa850612e1297dd93ddaa64499454030bba2340/README.md#L525-L529) | A configurable hotkey or push-to-talk records, transcribes, and pastes into the current app. [Dictation flow](https://github.com/cjpais/Handy/blob/6fa850612e1297dd93ddaa64499454030bba2340/README.md#L20-L33) | Speech transcription is fully local. AI post-processing is off by default. It can use cloud providers, a custom local OpenAI-compatible endpoint, or Apple Intelligence on supported Apple Silicon Macs. [Post-processing defaults and providers](https://github.com/cjpais/Handy/blob/6fa850612e1297dd93ddaa64499454030bba2340/src-tauri/src/settings.rs#L611-L715) | The core experience is raw transcription. Local LLM cleanup needs a separate local endpoint unless Apple Intelligence is available. |
| **VoiceInk** | The GPLv3 source is free to build. The ready-made app is a commercial build with a free trial. It requires macOS 14.4+. [Distribution model](https://github.com/Beingpax/VoiceInk/blob/68b871e79e2b1ec4c3b4914cccd2e0907d94237a/README.md#L42-L80), [dual-license terms](https://tryvoiceink.com/terms) | Global shortcuts start toggle or push-to-talk recording. A Mode can paste the final text into the active app. [VoiceInk workflow](https://tryvoiceink.com/docs/introduction), [output modes](https://tryvoiceink.com/docs/mode-settings) | Transcription can stay on-device. Enhancement can use Ollama, a local CLI, a custom endpoint, or a cloud provider with your key. [Model catalog](https://tryvoiceink.com/docs/ai-models), [enhancement settings](https://tryvoiceink.com/docs/mode-settings) | A free build needs Xcode. It omits automatic updates and iCloud dictionary sync, and ad-hoc builds may need permissions granted again after rebuilds. [Build guide](https://github.com/Beingpax/VoiceInk/blob/68b871e79e2b1ec4c3b4914cccd2e0907d94237a/BUILDING.md#L3-L34) |
| **macOS Dictation** | Included with macOS. No separate app, account, build, or subscription is required. | It inserts text anywhere you can type and uses a configurable shortcut. [Apple Mac User Guide](https://support.apple.com/en-gb/guide/mac-help/mh40584/26/mac/26) | On-device and offline availability depends on the Mac, language, and the status shown in Keyboard settings. Apple documents auto-punctuation and spoken formatting commands, not LLM rewriting or context-aware cleanup. [Processing and formatting behavior](https://support.apple.com/en-gb/guide/mac-help/mh40584/26/mac/26) | Language support varies. Dictation stops after 30 seconds of silence, although there is no total dictation-length limit. [Apple limits](https://support.apple.com/en-gb/guide/mac-help/mh40584/26/mac/26) |
| **VoiceScribe** | Fully free and MIT licensed. Signed and notarized builds support macOS 14+ on Apple Silicon. [Install and requirements](https://github.com/eddmann/VoiceScribe/blob/d1e29a10d52913c67458d695d417517d17162d9a/README.md#L90-L125), [license](https://github.com/eddmann/VoiceScribe/blob/d1e29a10d52913c67458d695d417517d17162d9a/README.md#L153-L166) | A global hotkey records from any app, then copies or auto-pastes the result. [Workflow](https://github.com/eddmann/VoiceScribe/blob/d1e29a10d52913c67458d695d417517d17162d9a/README.md#L7-L27) | Whisper or Parakeet handles local transcription. Optional MLX models handle local punctuation and formatting cleanup. There are no accounts, API keys, usage fees, or cloud path. [Local cleanup models](https://github.com/eddmann/VoiceScribe/blob/d1e29a10d52913c67458d695d417517d17162d9a/README.md#L59-L80), [privacy](https://github.com/eddmann/VoiceScribe/blob/d1e29a10d52913c67458d695d417517d17162d9a/README.md#L153-L162) | Apple Silicon only. The first model download needs internet. |

## Recommendation by priority

- **Best default.** OpenWhispr is the closest no-cost replacement with an
  easy Mac install, unlimited local use, cleanup, and cross-platform support.
- **Best fully open local cleanup.** Pick VoiceScribe if you have Apple
  Silicon.
- **Best simple dictation.** Pick Handy when reliable offline speech-to-text
  matters more than automatic rewriting.
- **Lowest-effort option.** Try macOS Dictation before installing anything
  if auto-punctuation is enough.
- **Free only as a source build.** VoiceInk becomes free only
  when you build the GPL source yourself.
