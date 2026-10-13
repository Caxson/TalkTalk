# TalkTalk design system

Family: **Cult / Indie**, with Data-Dense number discipline.
The conversation is presented as an archival interview transcript: italic
serif voices, wide-tracked mono labels, hairline rules, a numbering system
for turns, timecodes. One live accent (the tally light) and nothing else.

## Color tokens

| Token | Dark (default) | Light | Use |
|---|---|---|---|
| `--ground` | `#111010` | `#EDE8E0` | page background |
| `--ink` | `#EDE8E0` | `#161413` | primary text |
| `--muted` | `#8A847B` | `#6B655C` | labels, interim text |
| `--rule` | `#2C2926` | `#CFC8BC` | hairlines, meters background |
| `--tally` | `#E5482F` | `#C8361F` | live / recording only |

The tally colour appears only when something is live (connected dot,
speaking state, active meter). Never decorative.

## Type

| Role | Latin | Chinese |
|---|---|---|
| Display / voices | Instrument Serif (italic) | Songti SC → Noto Serif SC → STSong |
| Labels / data | IBM Plex Mono, `letter-spacing: .18em`, uppercase | PingFang SC |
| Numbers | IBM Plex Mono, `tabular-nums` | — |

Fonts are self-hosted (`internal/web/static/fonts`, OFL) so the page works
without Google Fonts, which is unreliable in mainland China.

## Shape and space

- Spacing base 8px; generous outer margins (48px desktop, 16px mobile).
- Zero border radius. 1px hairline rules, no shadows, no cards.
- Numbering (`No. 07`) is used only for conversation turns, which are a
  real sequence.

## The one bold thing

The avatar, presented as a framed film still: hairline frame, corner marks,
tiny mono slate marks (`TAKE 01`, fps, sync offset). The state word
(*listening* / *thinking* / *speaking* / *yielding*) becomes its italic
caption. Corner marks turn tally-red only while the assistant is audible.

The default avatar is a full-colour anime (VRM) character. It is the only
saturated thing on the page, which is the point: the monochrome archive
frames one living colour. The slate marks get a translucent ground plate so
they stay legible over hair and clothes.

Without an avatar (assets not downloaded), the state word itself is the
bold thing, set huge in italic serif.
