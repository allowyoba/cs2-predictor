# Figma handoff — Predictor Multi-Game Analyst 2.1

## Pages

### 00 Cover
Product, version, platform, status.

### 01 Foundations
Core colors, game accents, typography, spacing, radius, safe-area examples.

### 02 Components
Create component sets:
- `Navigation/Bottom Item` — default / active
- `Navigation/Game Chip` — default / active, CS2 / Dota2 / Valorant / LoL
- `Card/Performance`
- `Card/Portfolio`
- `Card/Coach`
- `Card/Achievement` — progress / earned / locked
- `Row/Match`
- `Row/Segment`
- `Team/Logo` — image / fallback
- `Badge/Result` — correct / wrong / neutral
- `Chip/Filter` — default / selected
- `Header/App`

### 03 Screens / Mobile 390
- Dashboard / CS2
- Dashboard / Dota 2
- Dashboard / Valorant
- Dashboard / LoL
- Analytics / CS2
- History / All games
- Achievements / Multi-game
- Profile / Multi-game
- Loading / Empty / Error variants

### 04 Prototype
Flows:
1. Switch game → Dashboard metrics update
2. Dashboard → Analytics
3. Dashboard → History
4. History → filter sheet
5. Achievement → detail
6. Profile → discipline statistics

## Frame & grid

- Base: 390×844
- 4 columns, margin 16, gutter 12
- Test compact frame: 320×844
- All content uses Auto Layout; page sections hug height
- Bottom nav visually fixed to safe bottom
- Game rail horizontal scroll with clip content

## Variables

`Color/Core`: bg, surfaces, borders, text, success, danger, warning.

`Color/Game`: cs2, dota2, valorant, lol.

Use variable alias `accent/current` at screen/frame level rather than duplicating component variants for every color where possible.

`Number`: spacing 4/8/12/16/20/24/32, radius 10/13/15/18/22/24.

## Auto Layout requirements

- No text layer should be absolutely positioned.
- Dense rows: `Fill container` text group + fixed logo/result columns.
- Set long text containers to Fill, not fixed width.
- Match team names: one-line ellipsis on compact row; wrap in detail.
- Portfolio: 2-column mobile grid; cards hug content.
- Achievement and Coach: vertical Auto Layout, hug height.

## Import/reference assets

- `figma/predictor-multigame-board.png` — visual board of five core screens.
- `figma/v2/viewport-final/*.png` — 390×844 screen references.
- `miniapp-prototype/` — source-of-truth interactive reference for spacing/state behavior.
- `miniapp-prototype/assets/teams/cs2/team-logos.json` — CS2 team-logo mapping based on HLTV ranking source.

The PNG board is for visual reference. Build production Figma screens from the component/variable rules above or import the HTML implementation through the team's preferred design-to-code workflow; do not trace screenshots as final components.
