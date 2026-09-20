# Predictor — Multi-Game Analyst / Design System 2.1

## Направление

Современный dark analytics UI: professional esports data product, а не стилизация под HUD конкретной игры. Визуальная идентичность строится на типографике, data density, restrained gradients, clear semantic states и game accent token. Компоненты одинаково работают для CS2, Dota 2, Valorant, League of Legends и будущих дисциплин.

## Core colors

- `bg/base` — `#07090E`
- `bg/elevated` — `#0B0E15`
- `surface/1` — `#111621`
- `surface/2` — `#151C29`
- `surface/3` — `#1A2231`
- `border/default` — `rgba(164,183,214,.13)`
- `border/strong` — `rgba(164,183,214,.22)`
- `text/primary` — `#F5F7FB`
- `text/secondary` — `#A8B2C2`
- `text/muted` — `#6F7B8D`
- `success` — `#79F2A1`
- `danger` — `#FF6B87`
- `warning` — `#FFCB6B`

## Game accent tokens

Game color is context, not semantic status. Success/error never inherit game color.

- `game/cs2` — `#46E6FF`
- `game/dota2` — `#FF6B62`
- `game/valorant` — `#FF5864`
- `game/lol` — `#D6AF57`

All accent surfaces use 6–12% alpha. Large neon fills are prohibited.

## Typography

System sans / Inter. Tabular numerals for metrics.

- Metric XL: 56/54, 800–850
- H1: 25–31 / 1.1, 750
- H2: 18/22, 700
- Body: 13–15 / 1.45
- Label: 11–12 / 16, 700
- Micro: 8–10 / 13, 800, uppercase, 8–11% tracking

## Layout

- Base frame: 390×844
- Supported mobile widths: 320–560
- Page inset: 16 px; 12 px below 360 width
- Section gap: 22–24 px
- Component gap: 8–12 px
- 4 pt spacing grid
- Cards use fluid height; never fix text block height
- All flex/grid content children must have `min-width: 0`
- Long rails use intentional horizontal scroll; normal page content must never create body horizontal overflow

## Radius

- Small control: 10–13
- Standard card: 15–18
- Hero: 22
- Bottom sheet: 24 top corners
- Pill: 999

## Core components

### GameRail
Global discipline context. Horizontal, scrollable, sticky below app header. Each item includes game dot, name and compact current accuracy. Active state uses game accent.

### PerformanceCard
Primary period accuracy + transparent form score + trend + compact spark bars. The form score must be explainable from existing metrics; do not present opaque betting-style probability.

### PortfolioCard
Cross-game comparison. Always names the discipline, accuracy and sample. Game color identifies context only.

### TeamLogo
For CS2: 28×28 compact / 38×38 match card. Logo sits on neutral light plate for legibility. Includes initials fallback if image fails. Production assets should be cached internally.

### MatchRow / Matchup
Team logos/monograms, competition metadata, score, prediction status. Never rely on red/green alone: status also includes symbol and text.

### SegmentRow
`segment name + correct/total + bar + percent`. Always expose sample size. Bars use direct labels rather than legends.

### CoachCard
One actionable observation. Structure: finding → measured evidence → next action. No unsupported causal wording.

### AchievementCard
Supports universal multi-game and discipline-specific awards. Tier/progress/earned states are separate variants.

### BottomNav
Five equal-width items, safe-area aware, min 53 px interactive height. Active game accent changes with global game context.

## Responsive / overflow rules

- Use `minmax(0, 1fr)` for grid columns containing text.
- Team/event names use ellipsis only in dense rows; detail screens should wrap.
- Bottom navigation is fixed; main content reserves `84px + safe-bottom` so the final row is reachable.
- Game rail and filter rail are the only intentional horizontal scrollers.
- At ≤359px reduce page padding and card gaps; do not shrink primary metric below usable reading size.
- No absolute positioning for text blocks. Absolute layers are decorative only and clipped by card boundaries.

## Motion

160–220 ms local transitions, no looping decoration. Respect `prefers-reduced-motion`. Haptics are optional enhancement through Telegram WebApp API.

## Data visualization

- Always show `n` or `correct / total`.
- Period comparisons use percentage points (`п.п.`), not relative percent unless explicitly stated.
- Do not use donut/pie for exact small comparisons; bars + direct labels are preferred.
- Avoid rankings or coach conclusions when sample is below product threshold.

## Accessibility

- Target WCAG AA contrast for body text.
- Controls min 38–44 px depending on density; primary navigation ≥53 px.
- Focus-visible ring uses active game accent.
- Result states combine text/icon/color.
- Team logo `alt` equals canonical team name.
