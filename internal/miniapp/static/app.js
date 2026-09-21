/**
 * Predictor Mini App — prototype shell.
 *
 * The screens, the game rail and the demo figures are still a prototype:
 * they exist to try the interface, not to report anything true. The one
 * part that is real is the team crests — those come from this bot's own
 * API (`GET /api/miniapp/v1/teams`), which serves what the match sync and
 * the HLTV ranking already collected.
 *
 * Three rules this file keeps, because a prototype is where they get set:
 *
 *  1. Nothing from the network is ever written into innerHTML. Team names
 *     are somebody else's data, and a prototype that interpolates them is
 *     the version that ships.
 *  2. Every remote read has a timeout, an error state and a fallback. A
 *     Mini App opens on a phone in a stadium; "it hung" is the failure
 *     mode to design for.
 *  3. Nothing is shown as a fact that the backend does not hold. Demo
 *     numbers are labelled as such.
 */
(() => {
  'use strict';

  const tg = window.Telegram?.WebApp;

  /** Telegram's own chrome: colours, expansion, and the platform back button. */
  function initTelegram() {
    if (!tg) return;
    try {
      tg.ready();
      tg.expand();
      tg.setHeaderColor?.('#07090E');
      tg.setBackgroundColor?.('#07090E');
      tg.setBottomBarColor?.('#090C12');
    } catch (_) {
      // An older Telegram client simply lacks these; the page still works.
    }
  }

  /** haptic fires Telegram's own feedback where available, silently elsewhere. */
  function haptic(kind = 'selection') {
    try {
      if (kind === 'selection') tg?.HapticFeedback?.selectionChanged?.();
      else tg?.HapticFeedback?.impactOccurred?.('light');
    } catch (_) {
      /* not fatal: feedback is a nicety */
    }
  }

  // --- data -----------------------------------------------------------

  /**
   * Three outcomes, not two: calling the winner and calling the exact
   * scoreline are worth different numbers of points, and showing both as
   * a plain tick hides the harder one on the screen that lists it.
   */
  const RESULT_TEXT = { exact: '🎯 Точный счёт', correct: '✓ Верно', wrong: '× Ошибка' };

  /** resultOf names which of the three a row is. */
  function resultOf(entry) {
    if (!entry.correct) return 'wrong';
    return entry.predicted && entry.predicted === entry.actual ? 'exact' : 'correct';
  }

  const params = new URLSearchParams(location.search);
  /**
   * The API lives on the same origin in production (Caddy proxies
   * /api/miniapp/*). `?api=` exists so the prototype can be opened from a
   * laptop against a deployed bot.
   */
  const API_BASE = params.get('api') || '';
  /** Which crest set to ask for; mirrors the chat setting of the same name. */
  /**
   * logoSource is which crests this person asked for, as the dashboard
   * reports it. A personal setting, made in the bot's own DM settings:
   * crests are only ever rendered here, so a chat-wide switch for them was
   * a control nobody could see the effect of. The query parameter is still
   * honoured so the page can be opened against a deployment by hand.
   */
  let logoSource = params.get('logos') === 'hltv' ? 'hltv' : 'provider';

  /**
   * logos maps a lowercased team name to its crest URL, per game.
   *
   * Every URL points back at this bot: the crests are mirrored server-side
   * so that opening this app never sends a request to HLTV's or
   * PandaScore's CDN. The page's own Content-Security-Policy refuses
   * images from anywhere else, so a third-party URL finding its way back
   * in here fails visibly instead of quietly resuming that traffic.
   */
  const logos = new Map();

  /**
   * chips maps a lowercased team name to the background its crest needs.
   *
   * There is no one colour that shows every logo: esports marks are either
   * light wordmarks drawn for dark backgrounds or dark ones drawn for
   * light, and a single chip loses half of them. The bot measures each
   * crest when it mirrors it and says which chip to use; a crest it could
   * not measure gets the neutral default.
   */
  const chips = new Map();

  /**
   * fetchJSON is every network read here: bounded, and never throwing past
   * the caller.
   *
   * Personal reads carry Telegram's own signed launch parameters, which is
   * the only credential this app has — there is no token of ours to store,
   * and nothing to leak if the page is opened anywhere else.
   */
  async function fetchJSON(path, { timeoutMs = 6000, signed = false, method = 'GET', body } = {}) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const headers = { Accept: 'application/json' };
    if (signed) {
      const initData = tg?.initData || '';
      if (!initData) throw new UnauthenticatedError();
      headers.Authorization = `tma ${initData}`;
    }
    const options = { method, signal: controller.signal, headers };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      options.body = JSON.stringify(body);
    }
    try {
      const response = await fetch(`${API_BASE}${path}`, options);
      if (response.status === 401) throw new UnauthenticatedError();
      if (response.status === 403) throw new ForbiddenError(await response.json().catch(() => ({})));
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      return await response.json();
    } finally {
      clearTimeout(timer);
    }
  }

  /** The two refusals the API can return, told apart so the screen can. */
  class UnauthenticatedError extends Error {}
  class ForbiddenError extends Error {
    constructor(body) {
      super('forbidden');
      this.access = body || {};
    }
  }

  /**
   * loadLogos fills the crest map for one game. A failure is not an error
   * state on screen: the team component falls back to initials, which is
   * what it does for an unranked team anyway.
   */
  async function loadLogos(game) {
    if (!game || logos.has(game)) return;
    logos.set(game, new Map());
    try {
      const body = await fetchJSON(`/api/miniapp/v1/teams?game=${encodeURIComponent(game)}&logos=${logoSource}`);
      const byName = logos.get(game);
      for (const team of body.teams || []) {
        if (!team.name || !team.logo) continue;
        byName.set(team.name.toLowerCase(), team.logo);
        if (team.chip) chips.set(team.name.toLowerCase(), team.chip);
      }
    } catch (error) {
      console.warn('team crests unavailable, falling back to initials', error);
    }
  }

  /**
   * logoFor looks a crest up in the game's own map, then in every other
   * loaded one.
   *
   * A feed mixes disciplines: the history screen shows CS2 and Dota 2 rows
   * together, and looking only in the selected game's map is why those
   * rows came out with initials where a crest belonged.
   */
  function logoFor(game, teamName) {
    if (!teamName) return '';
    const key = teamName.toLowerCase();
    const own = logos.get(game)?.get(key);
    if (own) return own;
    for (const byName of logos.values()) {
      const found = byName.get(key);
      if (found) return found;
    }
    return '';
  }

  /** initialsOf is the fallback mark: the first letters of a team's words. */
  function initialsOf(name) {
    const words = name.split(/\s+/).filter(Boolean);
    const letters = words.length > 1 ? words.slice(0, 2).map((w) => w[0]) : [name.slice(0, 2)];
    return letters.join('').toUpperCase();
  }

  // --- rendering ------------------------------------------------------
  //
  // Everything below builds DOM nodes rather than HTML strings. Team names
  // come from a provider, and the moment they are interpolated into markup
  // this prototype becomes the thing somebody ships.

  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  /** teamMark renders one team's crest with an initials fallback. */
  /**
   * teamMark draws a team's crest, or its initials when there is no crest
   * to draw.
   *
   * One or the other, never both. The initials sit behind the image as a
   * fallback, so until the image has actually painted they must stay
   * visible — and the moment it has, they must not: a crest is rarely a
   * filled square, and letters showing around its edges and through its
   * transparent parts read as a rendering fault.
   */
  function teamMark(game, name) {
    const wrap = el('span', 'team-logo');
    const initials = el('b', null, initialsOf(name));
    const url = logoFor(game, name);
    if (url) {
      const img = el('img');
      img.alt = '';
      img.loading = 'lazy';
      img.decoding = 'async';
      const settle = (loaded) => wrap.classList.toggle('has-crest', loaded);
      img.addEventListener('load', () => settle(true));
      // A crest that fails to load leaves the initials in place rather
      // than a broken-image glyph.
      img.addEventListener('error', () => {
        img.remove();
        settle(false);
      });
      const chip = chips.get(name?.toLowerCase?.() || '');
      if (chip) wrap.classList.add('chip-' + chip);
      wrap.append(img);
      // Set last: a cached image can fire load before the handler exists.
      img.src = url;
      // Cached images may have completed before any of this ran.
      if (img.complete && img.naturalWidth > 0) settle(true);
    }
    wrap.append(initials);
    return wrap;
  }

  function matchRow(entry) {
    const row = el('article', 'match-row');
    const main = el('div', 'match-main');
    const teams = el('div', 'match-teams');
    const game = entry.game.toLowerCase();
    teams.append(teamMark(game, entry.first_team), el('strong', null, entry.first_team),
      el('span', 'vs', 'vs'), teamMark(game, entry.second_team), el('strong', null, entry.second_team));
    const meta = el('div', 'match-meta');
    meta.append(el('span', null, entry.event || gameLabel(entry.game)), el('span', null, '·'),
      el('span', null, new Date(entry.played_at).toLocaleDateString('ru-RU', { day: '2-digit', month: 'short' })));
    main.append(teams, meta);

    const result = el('div', `match-result ${entry.correct ? 'correct' : 'wrong'}`);
    result.append(el('strong', null, entry.actual),
      el('span', null, RESULT_TEXT[resultOf(entry)]));
    row.append(main, result);
    return row;
  }

  const setText = (selector, value) => {
    const node = document.querySelector(selector);
    if (node) node.textContent = value;
  };

  // --- the real numbers ------------------------------------------------
  //
  // Everything on these screens is the person's own record, read from the
  // bot's database through a signed launch. Nothing is seeded, modelled or
  // filled in: when a figure cannot be loaded — no access yet, no network
  // — the screen says so instead of showing a number that means nothing.

  let dashboard = null;
  /**
   * scope is the app's one filter, and the only one. Every screen renders
   * what it describes and every request carries it, so "CS2" cannot mean
   * one thing on the history page and another on the awards page — which
   * is what happened while each screen narrowed its own data.
   *
   * Empty game means every discipline; null chat means every chat.
   */
  const scope = { game: '', chat: null };

  /** currentGame is which crest map to look in; the rail's pick, or the
   * only discipline there is. */
  let currentGame = '';

  /** scopeQuery renders the filter as the query string every endpoint
   * reads with the same parser. */
  function scopeQuery(extra = {}) {
    const query = new URLSearchParams();
    if (scope.game) query.set('game', scope.game.toUpperCase());
    if (scope.chat !== null) query.set('chat', String(scope.chat));
    for (const [key, value] of Object.entries(extra)) {
      if (value) query.set(key, value);
    }
    const rendered = query.toString();
    return rendered ? `?${rendered}` : '';
  }

  /**
   * GAME_LABELS names a discipline the way people say it. The API answers
   * with the bot's own codes; nothing else in the app should have to know
   * what those look like.
   */
  const GAME_LABELS = { CS2: 'CS2', DOTA2: 'Dota 2' };
  const gameLabel = (code) => GAME_LABELS[code] || code;

  /** percentText renders a share the way every screen here shows one. */
  const percentText = (value) => `${value}%`;

  /**
   * MIN_SEGMENT_SAMPLE is how many predictions a segment needs before its
   * percentage is worth naming as best or worst. Below it the number is a
   * coin toss with a label on it.
   */
  const MIN_SEGMENT_SAMPLE = 10;

  /**
   * MIN_TEAM_SAMPLE mirrors the API's own threshold for naming a team at
   * all — scoring.InsightsTeamMinPredictions. Stated on screen so an empty
   * table reads as "not enough yet" rather than "this is broken".
   */
  const MIN_TEAM_SAMPLE = 3;

  /** applyDashboard overwrites the headline figures with real ones. */
  function applyDashboard(data) {
    dashboard = data;
    // Applied before any crest is fetched: asking for the wrong source and
    // correcting it afterwards would be two requests and a visible flicker.
    if (data.user?.logo_source === 'hltv' || data.user?.logo_source === 'provider') {
      logoSource = data.user.logo_source;
    }
    const summary = data.summary || {};
    setText('#accuracyValue', summary.accuracy ?? 0);
    setText('#accuracySub', `${summary.correct ?? 0} верных из ${summary.predictions ?? 0} прогнозов`);
    setText('#sampleValue', summary.predictions ?? 0);
    setText('#streakValue', data.form?.current_streak ?? 0);
    setText('#bestStreakValue', data.form?.best_streak ?? 0);
    renderFormStrip(data.form?.recent || []);
    // The ring is the number: it used to be drawn at a fixed 82% whatever
    // the figure inside it said.
    const orbit = document.querySelector('#formOrbit');
    if (data.form?.recent?.length) {
      const wins = data.form.recent.filter(Boolean).length;
      const share = Math.round((wins / data.form.recent.length) * 100);
      setText('#formScore', share);
      orbit?.style.setProperty('--form-fill', `${share}%`);
    } else {
      setText('#formScore', '—');
      orbit?.style.setProperty('--form-fill', '0%');
    }
    const trend = data.trend;
    setText('#trendValue', trend
      ? `${trend.delta_pp > 0 ? '↗ +' : trend.delta_pp < 0 ? '↘ −' : '→ '}${Math.abs(trend.delta_pp)} п.п.`
      : '— недостаточно данных');
    renderGameRail(data.games || []);
    renderAnalytics(data);
    renderAchievements(data);
    renderProfile(data);
  }

  // --- analytics --------------------------------------------------------

  /**
   * renderAnalytics draws the breakdown for whatever the rail has
   * selected: one discipline, or all of them together.
   *
   * The rail used to change only which crests were fetched, so tapping
   * Dota 2 left a Counter-Strike breakdown on screen — the numbers never
   * moved, which is worse than not offering the filter at all.
   */
  function renderAnalytics(data) {
    // The rail's own options, which the API deliberately does not narrow.
    const all = [...(data.games || [])].filter((g) => g.predictions > 0);
    const games = scope.game ? all.filter((g) => g.game.toLowerCase() === scope.game) : all;

    setText('#analyticsGameLabel', scope.game && games.length
      ? gameLabel(games[0].game)
      : (all.length ? 'все игры' : '—'));
    setText('#analyticsGameCount', games.length || '—');
    const total = games.reduce((sum, g) => sum + g.predictions, 0);
    setText('#analyticsCoverage', total ? `${total} прогнозов` : 'нет данных');

    // Comparing disciplines needs two of them. With one — or with one
    // selected — "strongest" and "weakest" name the same thing twice and
    // the pair says nothing at all.
    const comparable = all.length > 1 && !scope.game;
    const overview = document.querySelector('#analyticsOverview');
    if (overview) overview.dataset.comparing = String(comparable);
    if (comparable) {
      // Only worth naming where the sample can support the claim; below
      // that a percentage is a coin toss with a label on it.
      const ranked = games.filter((g) => g.predictions >= MIN_SEGMENT_SAMPLE)
        .sort((a, b) => b.accuracy - a.accuracy);
      const best = ranked[0];
      const worst = ranked.length > 1 ? ranked[ranked.length - 1] : null;
      setText('#analyticsBest', best ? gameLabel(best.game) : '—');
      setText('#analyticsBestNote', best ? `${percentText(best.accuracy)} на ${best.predictions}` : `нужно ${MIN_SEGMENT_SAMPLE}+ прогнозов`);
      setText('#analyticsWorst', worst ? gameLabel(worst.game) : '—');
      setText('#analyticsWorstNote', worst ? `${percentText(worst.accuracy)} на ${worst.predictions}` : 'пока не с чем сравнивать');
    }

    const gamesRoot = document.querySelector('#analyticsGames');
    if (gamesRoot) {
      gamesRoot.replaceChildren(...games.map((entry) => {
        const row = el('div', 'segment-row' + (entry.accuracy < 50 ? ' is-warning' : ''));
        const copy = el('div', 'segment-copy');
        copy.append(el('strong', null, gameLabel(entry.game)), el('small', null, `${entry.predictions} прогнозов`));
        const meter = el('div', 'segment-meter');
        const fill = el('i');
        fill.style.width = `${Math.max(0, Math.min(100, entry.accuracy))}%`;
        meter.append(fill);
        row.append(copy, meter, el('b', null, percentText(entry.accuracy)));
        return row;
      }));
      if (!games.length) gamesRoot.replaceChildren(emptyLine('Пока нет завершённых прогнозов.'));
    }

    renderScatter(data.teams || []);
    renderBias(data.bias || []);
    renderCrossGame(all, recentEntries);
    renderTeamTable('#analyticsTeams', data.best_teams || []);
    renderTeamTable('#analyticsWorstTeams', data.worst_teams || []);
    // Two tables with nothing in either is one empty state, not two.
    const teamsEmpty = !(data.best_teams || []).length && !(data.worst_teams || []).length;
    const worstCard = document.querySelector('#worstTeamsCard');
    if (worstCard) worstCard.hidden = teamsEmpty || !(data.worst_teams || []).length;
  }

  /** renderTeamTable draws one end of the team ranking. Both ends use the
   * same row so a team looks the same wherever it turns up. */
  function renderTeamTable(selector, teams) {
    const root = document.querySelector(selector);
    if (!root) return;
    if (!teams.length) {
      root.replaceChildren(emptyLine(`Команда появится здесь, когда прогнозов по ней станет достаточно (от ${MIN_TEAM_SAMPLE}).`));
      return;
    }
    root.replaceChildren(...teams.map((team, index) => {
      const row = el('div', 'team-row' + (team.accuracy < 50 ? ' low' : ''));
      row.append(el('span', 'rank-num', String(index + 1).padStart(2, '0')), teamMark(currentGame, team.team));
      const copy = el('div', 'team-copy');
      copy.append(el('strong', null, team.team), el('small', null, `${team.predictions} прогнозов`));
      row.append(copy, el('em', null, percentText(team.accuracy)));
      return row;
    }));
  }

  // --- where a percentage is worth believing ---------------------------

  /**
   * renderScatter plots accuracy against how many predictions it rests on.
   *
   * A percentage on its own hides its own reliability: 91% over seven
   * predictions and 85% over thirty-four look like a ranking, and are not.
   * Two axes separate the claim from the evidence behind it, which is the
   * only honest way to show both at once.
   */
  function renderScatter(teams) {
    const root = document.querySelector('#teamScatter');
    if (!root) return;
    const points = (teams || []).filter((t) => t.predictions > 0);
    if (points.length < MIN_SCATTER_POINTS) {
      root.replaceChildren(emptyLine(`Появится, когда наберётся ${MIN_SCATTER_POINTS}+ команд с достаточной историей.`));
      return;
    }

    const width = 320;
    const height = 180;
    const pad = { left: 24, right: 10, top: 10, bottom: 20 };
    const maxN = Math.max(...points.map((p) => p.predictions));
    const x = (n) => pad.left + (n / maxN) * (width - pad.left - pad.right);
    const y = (a) => pad.top + ((100 - a) / 100) * (height - pad.top - pad.bottom);

    const ns = 'http://www.w3.org/2000/svg';
    const node = (name, attrs, text) => {
      const n = document.createElementNS(ns, name);
      for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
      if (text !== undefined) n.textContent = text;
      return n;
    };
    const svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
    svg.setAttribute('role', 'img');
    svg.setAttribute('aria-label', 'Точность команд против числа прогнозов по ним');

    // The 50% line is the only reference that means anything here: above
    // it a call is better than a coin, below it worse.
    svg.append(node('line', { x1: pad.left, x2: width - pad.right, y1: y(50), y2: y(50), class: 'scatter-mid' }));
    svg.append(node('text', { x: 2, y: y(50) + 3, class: 'scatter-tick' }, '50%'));
    svg.append(node('text', { x: 2, y: y(100) + 8, class: 'scatter-tick' }, '100%'));
    // The reliability threshold: to the right of it a percentage rests on
    // enough matches to be worth quoting.
    if (maxN > MIN_SEGMENT_SAMPLE) {
      svg.append(node('line', {
        x1: x(MIN_SEGMENT_SAMPLE), x2: x(MIN_SEGMENT_SAMPLE), y1: pad.top, y2: height - pad.bottom,
        class: 'scatter-threshold',
      }));
    }

    for (const point of points) {
      const solid = point.predictions >= MIN_SEGMENT_SAMPLE;
      const dot = node('circle', {
        cx: x(point.predictions), cy: y(point.accuracy),
        r: solid ? 5 : 3.5,
        class: 'scatter-dot' + (solid ? '' : ' thin') + (point.accuracy >= 50 ? '' : ' low'),
      });
      dot.append(node('title', {}, `${point.team}: ${point.accuracy}% на ${point.predictions} ${plural(point.predictions, 'прогнозе', 'прогнозах', 'прогнозах')}`));
      svg.append(dot);
    }
    root.replaceChildren(svg);
  }

  const MIN_SCATTER_POINTS = 3;

  /**
   * renderBias shows who somebody backs more often than that team wins,
   * and who they back less often than it does.
   *
   * Drawn as bars either side of a centre line, because the direction is
   * the finding: a team you back too rarely is as interesting as one you
   * back too often, and a ranked list would bury one of them.
   */
  function renderBias(rows) {
    const card = document.querySelector('#biasCard');
    const root = document.querySelector('#teamBias');
    if (!card || !root) return;
    const entries = rows || [];
    if (entries.length === 0) {
      card.hidden = true;
      return;
    }
    card.hidden = false;
    const widest = Math.max(...entries.map((e) => Math.abs(e.bias_pp)), 1);

    root.replaceChildren(...entries.map((entry) => {
      const row = el('div', 'bias-row');
      const name = el('div', 'bias-name');
      name.append(teamMark(entry.game ? entry.game.toLowerCase() : currentGame, entry.team), el('strong', null, entry.team));

      const track = el('div', 'bias-track');
      const bar = el('i', 'bias-bar ' + (entry.bias_pp >= 0 ? 'over' : 'under'));
      bar.style.width = `${(Math.abs(entry.bias_pp) / widest) * 50}%`;
      track.append(el('i', 'bias-axis'), bar);

      const value = el('div', 'bias-value ' + (entry.bias_pp >= 0 ? 'over' : 'under'));
      value.append(el('b', null, `${entry.bias_pp >= 0 ? '+' : '−'}${Math.abs(entry.bias_pp)} п.п.`),
        el('small', null, `вы ${entry.pick_rate}% · они ${entry.win_rate}%`));

      row.append(name, track, value);
      row.title = `${entry.team}: выбираете в ${entry.pick_rate}% матчей, выигрывают ${entry.win_rate}%, ваша точность ${entry.accuracy}% на ${entry.matches}`;
      return row;
    }));
  }

  /**
   * renderCrossGame puts the disciplines side by side.
   *
   * One number per tab answers "how am I doing in this game" and never
   * "in which game am I improving", which is the question somebody with
   * two games actually has.
   */
  function renderCrossGame(games, entries) {
    const card = document.querySelector('#crossGameCard');
    const root = document.querySelector('#crossGame');
    if (!card || !root) return;
    const played = (games || []).filter((g) => g.predictions > 0);
    if (played.length < 2) {
      card.hidden = true;
      return;
    }
    card.hidden = false;

    root.replaceChildren(...played.map((game) => {
      const cell = el('article', 'multiple');
      cell.append(el('div', 'eyebrow', gameLabel(game.game)));
      cell.append(el('strong', null, percentText(game.accuracy)));

      const recent = (entries || [])
        .filter((e) => e.game === game.game)
        .slice(0, SPARK_LENGTH)
        .reverse();
      cell.append(sparkline(recent));

      const trend = game.trend;
      const delta = trend ? trend.delta_pp : 0;
      cell.append(el('small', trend ? (delta >= 0 ? 'positive-text' : 'negative-text') : 'muted',
        trend ? `${delta >= 0 ? '+' : '−'}${Math.abs(delta)} п.п.` : `${game.predictions} ${plural(game.predictions, 'прогноз', 'прогноза', 'прогнозов')}`));
      return cell;
    }));
  }

  const SPARK_LENGTH = 12;

  /** sparkline draws a run of results as a shape, not a number. */
  function sparkline(entries) {
    const strip = el('div', 'spark');
    if (entries.length === 0) {
      strip.append(el('i', 'spark-mark empty'));
      return strip;
    }
    strip.append(...entries.map((entry) => el('i', 'spark-mark ' + (entry.correct ? 'won' : 'lost'))));
    return strip;
  }

  /**
   * renderTournaments ranks tournaments by how the person does in them,
   * against their own average rather than against an absolute.
   *
   * 64% means nothing on its own; 64% from somebody who averages 77% is
   * the finding.
   */
  function renderTournaments(entries) {
    const card = document.querySelector('#tournamentCard');
    const root = document.querySelector('#tournamentTable');
    if (!card || !root) return;

    const byEvent = new Map();
    for (const entry of entries || []) {
      if (!entry.event) continue;
      const row = byEvent.get(entry.event) || { event: entry.event, tier: entry.tier || '', total: 0, correct: 0 };
      row.total += 1;
      if (entry.correct) row.correct += 1;
      byEvent.set(entry.event, row);
    }
    const rows = [...byEvent.values()].filter((r) => r.total >= MIN_TOURNAMENT_SAMPLE);
    if (rows.length < 2) {
      card.hidden = true;
      return;
    }
    card.hidden = false;

    const total = entries.length;
    const overall = Math.round((entries.filter((e) => e.correct).length / Math.max(1, total)) * 100);
    for (const row of rows) row.accuracy = Math.round((row.correct / row.total) * 100);
    rows.sort((a, b) => b.accuracy - a.accuracy);

    root.replaceChildren(...rows.map((row) => {
      const line = el('article', 'tournament-row');
      const copy = el('div');
      const title = el('strong', null, row.event);
      copy.append(title);
      copy.append(el('small', null, `${row.total} ${plural(row.total, 'прогноз', 'прогноза', 'прогнозов')}${row.tier ? ' · ' + tierLabel(row.tier) : ''}`));
      const delta = row.accuracy - overall;
      line.append(copy, el('b', null, percentText(row.accuracy)),
        el('em', delta >= 0 ? 'positive-text' : 'negative-text', `${delta >= 0 ? '+' : '−'}${Math.abs(delta)}`));
      return line;
    }));
  }

  const MIN_TOURNAMENT_SAMPLE = 4;

  /** tierLabel spells a tier the way the bot does. */
  function tierLabel(tier) {
    const badges = { s: '🌟 S-tier', a: '⭐ A-tier', b: 'B-tier', c: 'C-tier', d: 'D-tier' };
    return badges[String(tier).toLowerCase()] || String(tier).toUpperCase();
  }

  // --- the shape of a record -------------------------------------------
  //
  // Three views of the same settled predictions, each answering something
  // a single percentage cannot.
  //
  // All three are built from the history feed the app already loads: no
  // extra request, and nothing here can disagree with the list below it
  // because it is the same list.

  /** ROLLING_WINDOWS are the sizes the chart offers. */
  const ROLLING_WINDOWS = [20, 50, 100];
  let rollingWindow = 20;

  /**
   * renderForm draws the rolling accuracy, the streak timeline and the
   * calendar from one ordered list of settled predictions.
   *
   * entries arrive newest first, which is right for a feed and wrong for
   * everything here: a line that reads right-to-left is a line nobody
   * reads correctly.
   */
  function renderForm(entries) {
    const ordered = [...entries].reverse();
    renderRolling(ordered);
    renderStreakTimeline(ordered);
    renderCalendar(ordered);
    renderTournaments(entries);
    if (dashboard) renderCrossGame(dashboard.games || [], entries);
  }

  /**
   * renderRolling draws accuracy over the last N predictions against the
   * all-time average.
   *
   * The point of the chart is the gap between the two lines: a number on
   * its own says how somebody has done, and the distance from their own
   * average says whether they are doing it better or worse than usual.
   */
  function renderRolling(ordered) {
    const chart = document.querySelector('#rollingChart');
    const legend = document.querySelector('#rollingLegend');
    const switcher = document.querySelector('#rollingWindow');
    if (!chart || !legend || !switcher) return;

    // A window as long as the whole history draws one point and calls it
    // a trend. Windows that cannot produce a readable line are offered but
    // refused, with the reason on them, rather than silently missing.
    const usable = (size) => ordered.length - Math.min(size, ordered.length) + 1 >= MIN_ROLLING_POINTS;
    if (!usable(rollingWindow)) {
      rollingWindow = ROLLING_WINDOWS.filter(usable).pop() || ROLLING_WINDOWS[0];
    }
    switcher.replaceChildren(...ROLLING_WINDOWS.map((size) => {
      const ok = usable(size);
      const button = el('button', 'window-chip' + (size === rollingWindow ? ' active' : ''), String(size));
      button.setAttribute('aria-pressed', String(size === rollingWindow));
      button.disabled = !ok;
      if (!ok) button.title = `Нужно больше прогнозов: окно в ${size} пока покрывает всю историю целиком.`;
      button.addEventListener('click', () => {
        if (!ok) return;
        rollingWindow = size;
        haptic();
        renderForm(recentEntries);
      });
      return button;
    }));

    // Below the window there is no rolling average yet, only a shorter
    // and shorter prefix — which would draw a wild line out of two
    // predictions and call it form.
    if (ordered.length < MIN_ROLLING_SAMPLE) {
      chart.replaceChildren(emptyLine(`Нужно хотя бы ${MIN_ROLLING_SAMPLE} завершённых прогнозов, чтобы линия что-то значила.`));
      legend.replaceChildren();
      return;
    }

    const size = Math.min(rollingWindow, ordered.length);
    const series = [];
    let hits = 0;
    for (let i = 0; i < ordered.length; i += 1) {
      if (ordered[i].correct) hits += 1;
      if (i >= size) {
        if (ordered[i - size].correct) hits -= 1;
      }
      if (i >= size - 1) series.push(Math.round((hits / size) * 100));
    }
    const overall = Math.round((ordered.filter((e) => e.correct).length / ordered.length) * 100);

    chart.replaceChildren(rollingSvg(series, overall));

    const latest = series[series.length - 1];
    const earliest = series[0];
    const delta = latest - earliest;
    legend.replaceChildren(
      legendItem('accent', `${latest}% сейчас`),
      legendItem('muted', `${overall}% за всё время`),
      legendItem(delta >= 0 ? 'up' : 'down',
        `${delta >= 0 ? '+' : '−'}${Math.abs(delta)} п.п. за ${series.length} ${plural(series.length, 'прогноз', 'прогноза', 'прогнозов')}`),
    );
  }

  /** MIN_ROLLING_SAMPLE is the shortest history worth drawing a line for. */
  const MIN_ROLLING_SAMPLE = 12;

  /** MIN_ROLLING_POINTS is the fewest points that still read as a line
   * rather than as a dot with an axis around it. */
  const MIN_ROLLING_POINTS = 5;

  /**
   * plural picks the Russian form. "1 прогнозов" is the kind of wrong that
   * makes a careful screen look machine-made.
   */
  function plural(n, one, few, many) {
    const mod100 = n % 100;
    if (mod100 >= 11 && mod100 <= 14) return many;
    switch (n % 10) {
      case 1: return one;
      case 2: case 3: case 4: return few;
      default: return many;
    }
  }

  function legendItem(kind, text) {
    const item = el('span', 'legend-item ' + kind);
    item.append(el('i'), el('span', null, text));
    return item;
  }

  /**
   * rollingSvg draws the line. SVG rather than a canvas so it scales with
   * the phone and stays readable when the system font is enlarged.
   */
  function rollingSvg(series, overall) {
    const width = 320;
    const height = 120;
    const pad = 6;
    const lo = Math.max(0, Math.min(...series, overall) - 6);
    const hi = Math.min(100, Math.max(...series, overall) + 6);
    const span = Math.max(1, hi - lo);
    const x = (i) => pad + (i * (width - pad * 2)) / Math.max(1, series.length - 1);
    const y = (v) => height - pad - ((v - lo) / span) * (height - pad * 2);

    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
    svg.setAttribute('preserveAspectRatio', 'none');
    svg.setAttribute('role', 'img');
    svg.setAttribute('aria-label', `Скользящая точность: сейчас ${series[series.length - 1]}%, в среднем ${overall}%`);

    const node = (name, attrs) => {
      const n = document.createElementNS('http://www.w3.org/2000/svg', name);
      for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
      return n;
    };

    // The average first, so the line is read against it rather than over it.
    svg.append(node('line', { x1: pad, x2: width - pad, y1: y(overall), y2: y(overall), class: 'rolling-avg' }));

    const points = series.map((v, i) => `${x(i)},${y(v)}`).join(' ');
    svg.append(node('polyline', { points, class: 'rolling-line' }));
    svg.append(node('polygon', {
      points: `${x(0)},${height - pad} ${points} ${x(series.length - 1)},${height - pad}`,
      class: 'rolling-fill',
    }));
    svg.append(node('circle', { cx: x(series.length - 1), cy: y(series[series.length - 1]), r: 3.5, class: 'rolling-head' }));
    return svg;
  }

  /**
   * renderStreakTimeline shows the last results in order, so a run of
   * three and a run of nine do not look like the same "streak: 3".
   */
  function renderStreakTimeline(ordered) {
    const root = document.querySelector('#streakTimeline');
    const summary = document.querySelector('#streakSummary');
    if (!root) return;
    const tail = ordered.slice(-STREAK_TIMELINE_LENGTH);
    if (tail.length === 0) {
      root.replaceChildren(emptyLine('Здесь появится последовательность ваших результатов.'));
      return;
    }
    root.replaceChildren(...tail.map((entry) => {
      const mark = el('i', 'streak-mark ' + (entry.correct ? 'won' : 'lost'));
      const when = new Date(entry.played_at).toLocaleDateString('ru-RU', { day: '2-digit', month: 'short' });
      mark.title = `${when}: ${entry.first_team} — ${entry.second_team}, ${entry.correct ? 'верно' : 'ошибка'}`;
      return mark;
    }));

    let run = 0;
    for (let i = tail.length - 1; i >= 0 && tail[i].correct; i -= 1) run += 1;
    if (summary) summary.textContent = run > 0 ? `${run} подряд` : 'серия прервана';
  }

  const STREAK_TIMELINE_LENGTH = 30;

  /**
   * renderCalendar colours each day by how the day went rather than by how
   * much was predicted: a busy bad day and a busy good one are the same
   * square on a contribution graph, and opposite things here.
   */
  function renderCalendar(ordered) {
    const root = document.querySelector('#calendar');
    const summary = document.querySelector('#calendarSummary');
    if (!root) return;
    const byDay = new Map();
    for (const entry of ordered) {
      const day = new Date(entry.played_at);
      if (Number.isNaN(day.getTime())) continue;
      const key = day.toISOString().slice(0, 10);
      const cell = byDay.get(key) || { total: 0, correct: 0 };
      cell.total += 1;
      if (entry.correct) cell.correct += 1;
      byDay.set(key, cell);
    }
    if (byDay.size === 0) {
      root.replaceChildren(emptyLine('Появится, когда наберётся история по дням.'));
      return;
    }

    const days = [];
    const today = new Date();
    for (let back = CALENDAR_DAYS - 1; back >= 0; back -= 1) {
      const day = new Date(today);
      day.setDate(today.getDate() - back);
      const key = day.toISOString().slice(0, 10);
      days.push({ key, day, cell: byDay.get(key) });
    }
    root.replaceChildren(...days.map(({ key, day, cell }) => {
      const square = el('i', 'cal-cell lvl-' + calendarLevel(cell));
      const label = day.toLocaleDateString('ru-RU', { day: '2-digit', month: 'short' });
      square.title = cell
        ? `${label}: ${cell.correct} из ${cell.total}`
        : `${label}: прогнозов не было`;
      square.dataset.day = key;
      return square;
    }));
    const active = days.filter((d) => d.cell).length;
    if (summary) {
      summary.textContent = `${active} ${plural(active, 'день', 'дня', 'дней')} с прогнозами за ${CALENDAR_DAYS}`;
    }
  }

  const CALENDAR_DAYS = 70;

  /** calendarLevel maps a day's accuracy to one of four shades; a day with
   * nothing in it is its own, emptiest, shade. */
  function calendarLevel(cell) {
    if (!cell || cell.total === 0) return 'none';
    const share = cell.correct / cell.total;
    if (share >= 0.75) return 3;
    if (share >= 0.5) return 2;
    if (share > 0) return 1;
    return 0;
  }

  // --- active predictions ----------------------------------------------
  //
  // The half of somebody's record that has not happened yet: what they
  // have riding right now. This is what the app is opened for on a match
  // day, so it sits above the finished ones rather than behind a tab.

  async function loadActive() {
    const root = document.querySelector('#activeList');
    if (!root) return;
    try {
      const body = await fetchJSON(`/api/miniapp/v1/me/active${scopeQuery()}`, { signed: true });
      renderActive(body.entries || []);
      setText('#activeCount', body.count ? `${body.count}` : '');
    } catch (error) {
      if (error instanceof ForbiddenError || error instanceof UnauthenticatedError) return;
      console.warn('active predictions unavailable', error);
      root.replaceChildren(failedLine(describeFailure(error, 'активные прогнозы'), loadActive));
    }
  }

  function renderActive(entries) {
    const root = document.querySelector('#activeList');
    if (!root) return;
    if (entries.length === 0) {
      root.replaceChildren(emptyLine('Нет прогнозов в ожидании — они появятся, когда вы ответите на опрос.'));
      return;
    }
    root.replaceChildren(...entries.map(activeRow));
  }

  function activeRow(entry) {
    const row = el('article', 'match-row active-row');
    const main = el('div', 'match-main');
    const teams = el('div', 'match-teams');
    const game = entry.game.toLowerCase();
    teams.append(teamMark(game, entry.first_team), el('strong', null, entry.first_team),
      el('span', 'vs', 'vs'), teamMark(game, entry.second_team), el('strong', null, entry.second_team));

    const meta = el('div', 'match-meta');
    meta.append(el('span', null, entry.event || gameLabel(entry.game)), el('span', null, '·'),
      el('span', null, entry.live ? 'идёт сейчас' : startsAtText(entry.starts_at)));
    main.append(teams, meta);

    const side = el('div', 'match-result pending');
    side.append(el('strong', null, entry.predicted), el('span', null, 'ваш прогноз'));
    // The broadcast, when there is one: the single most useful thing to
    // offer somebody looking at a match they have money on.
    if (entry.stream) {
      const watch = el('a', 'stream-link', 'Смотреть');
      watch.href = entry.stream;
      watch.target = '_blank';
      watch.rel = 'noopener noreferrer';
      side.append(watch);
    }
    row.append(main, side);
    return row;
  }

  /** startsAtText says when, or says that nobody has published a time. */
  function startsAtText(value) {
    if (!value) return 'время уточняется';
    const at = new Date(value);
    const today = new Date();
    const sameDay = at.toDateString() === today.toDateString();
    const time = at.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
    if (sameDay) return `сегодня ${time}`;
    return `${at.toLocaleDateString('ru-RU', { day: '2-digit', month: 'short' })} ${time}`;
  }

  // --- chats -------------------------------------------------------------

  async function loadChats() {
    const root = document.querySelector('#chatStandings');
    if (!root) return;
    try {
      const body = await fetchJSON(`/api/miniapp/v1/me/chats${scopeQuery()}`, { signed: true });
      renderChats(body.chats || []);
      renderMedals(body.medals || []);
    } catch (error) {
      if (error instanceof ForbiddenError || error instanceof UnauthenticatedError) return;
      root.replaceChildren(failedLine(describeFailure(error, 'статистику по чатам'), loadChats));
    }
  }

  /**
   * renderMedals says what each medal is for. A count of three golds is a
   * number; "1st place, IEM Katowice, in Прогнозы, in March" is the thing
   * the number was standing in for, and it is what somebody actually
   * remembers winning.
   */
  const PLACE_ICON = { 1: '🥇', 2: '🥈', 3: '🥉' };
  const PLACE_NAME = { 1: '1 место', 2: '2 место', 3: '3 место' };

  function renderMedals(medals) {
    const root = document.querySelector('#medalList');
    if (!root) return;
    if (medals.length === 0) {
      root.replaceChildren(emptyLine(scopeIsNarrowed()
        ? 'В выбранной дисциплине и чате медалей пока нет.'
        : 'Медали появятся, когда в чате завершится турнир с вашими прогнозами.'));
      return;
    }
    root.replaceChildren(...medals.map((medal) => {
      const row = el('article', 'medal-card place-' + medal.place);
      row.append(el('div', 'medal-badge', PLACE_ICON[medal.place] || '🏅'));
      const copy = el('div');
      copy.append(el('strong', null, medal.event),
        el('small', null, `${PLACE_NAME[medal.place] || 'призовое место'} · ${medal.chat} · ${gameLabel(medal.game)}`));
      row.append(copy, el('time', 'medal-when', medalDate(medal.awarded_at)));
      return row;
    }));
  }

  function medalDate(value) {
    const at = new Date(value);
    if (Number.isNaN(at.getTime())) return '';
    return at.toLocaleDateString('ru-RU', { month: 'short', year: 'numeric' });
  }

  function renderChats(chats) {
    const root = document.querySelector('#chatStandings');
    if (!root) return;
    if (chats.length === 0) {
      chatMedalTotal = 0;
      root.replaceChildren(emptyLine(scopeIsNarrowed()
        ? 'В выбранном фильтре завершённых прогнозов пока нет.'
        : 'Здесь появятся ваши чаты, как только в них завершатся прогнозы.'));
      return;
    }
    chatMedalTotal = chats.reduce((sum, c) => sum + c.gold + c.silver + c.bronze, 0);
    if (dashboard) renderAchievements(dashboard);
    root.replaceChildren(...chats.map((chat) => {
      const row = el('article', 'chat-row');
      const copy = el('div', 'chat-copy');
      copy.append(el('strong', null, chat.chat),
        el('small', null, `${chat.predictions} прогнозов · ${percentText(chat.accuracy)} · ${chat.points} очков`));
      row.append(copy);
      const medals = el('div', 'medal-row');
      // Only the medals actually won: a row of zeroes reads as a scoreboard
      // of failures rather than as an empty cabinet.
      for (const [icon, count] of [['🥇', chat.gold], ['🥈', chat.silver], ['🥉', chat.bronze]]) {
        if (count > 0) medals.append(el('span', 'medal', `${icon} ${count}`));
      }
      if (!medals.childNodes.length) medals.append(el('span', 'medal muted', 'без медалей'));
      row.append(medals);
      return row;
    }));
  }

  // --- achievements -----------------------------------------------------
  //
  // Derived from the same figures, with the rule written on each card.
  // Nothing is invented: a badge is a threshold over a number the database
  // actually holds, and its progress is that number.

  /**
   * ACHIEVEMENTS is the whole catalogue, always shown in full.
   *
   * A list that hides what somebody has not earned answers "what have I
   * done" and never "what is there to do" — and the second question is the
   * one a progress screen exists for. Each entry carries the rule it is
   * measured by, so nothing here is a number without a reason.
   */
  const ACHIEVEMENTS = [
    { title: 'Первый прогноз', rule: 'Сделать первый завершённый прогноз', goal: 1, of: (s) => s.summary.predictions },
    { title: 'Полсотни', rule: '50 прогнозов', goal: 50, of: (s) => s.summary.predictions },
    { title: 'Сотня верных', rule: '100 верных прогнозов', goal: 100, of: (s) => s.summary.correct },
    { title: 'Снайпер', rule: '25 угаданных точных счётов', goal: 25, of: (s) => s.summary.exact },
    { title: 'Серия', rule: '5 верных подряд', goal: 5, of: (s) => s.form.best_streak },
    { title: 'Длинная серия', rule: '10 верных подряд', goal: 10, of: (s) => s.form.best_streak },
    { title: 'Мультигейм', rule: '≥65% в двух дисциплинах при 25+ прогнозах', goal: 2, of: (s) => s.strongGames },
    { title: 'Турнирный стаж', rule: 'Прогнозы в 10 турнирах', goal: 10, of: (s) => s.summary.tournaments },
    { title: 'Медалист', rule: 'Призовое место в турнире чата', goal: 1, of: (s) => s.medals },
    { title: 'Коллекция', rule: '5 призовых мест', goal: 5, of: (s) => s.medals },
  ];

  /** achievementInputs reduces everything loaded so far to the few numbers
   * the catalogue measures against. */
  function achievementInputs(data) {
    return {
      summary: data.summary || {},
      form: data.form || {},
      strongGames: (data.games || []).filter((g) => g.predictions >= 25 && g.accuracy >= 65).length,
      medals: chatMedalTotal,
    };
  }

  function achievementsFor(data) {
    const inputs = achievementInputs(data);
    return ACHIEVEMENTS.map((item) => ({
      title: item.title,
      rule: item.rule,
      goal: item.goal,
      value: Math.max(0, item.of(inputs) || 0),
    }));
  }

  /**
   * RECENT_ON_DASHBOARD is how much of the feed the overview shows. The
   * whole list lives one tap away on "История"; repeating it here would
   * make the overview a second history screen rather than a summary.
   */
  const RECENT_ON_DASHBOARD = 5;

  /** recentEntries is the unfiltered history feed, kept for the overview. */
  let recentEntries = [];

  /** renderRecent fills the overview's "Завершённые" strip, honouring the
   * discipline chosen in the rail. */
  function renderRecent() {
    const root = document.querySelector('#recentMatches');
    if (!root) return;
    // Already narrowed by the API: this is the same feed the history
    // screen shows, filtered once, on the way here.
    if (recentEntries.length === 0) {
      root.replaceChildren(emptyLine(scopeIsNarrowed()
        ? 'В выбранной дисциплине и чате завершённых прогнозов пока нет.'
        : 'Здесь появятся ваши прогнозы после первых завершённых матчей.'));
      return;
    }
    root.replaceChildren(...recentEntries.slice(0, RECENT_ON_DASHBOARD).map(matchRow));
  }

  /** renderAchievements shows the catalogue in full, earned or not, with
   * the progress towards each entry's own rule. */
  function renderAchievements(data) {
    const list = document.querySelector('#achievementList');
    const summary = document.querySelector('#achievementSummary');
    if (!list || !summary) return;

    const items = achievementsFor(data);
    const earned = items.filter((item) => item.value >= item.goal).length;
    summary.replaceChildren(
      summaryTile(String(earned), 'получено'),
      summaryTile(String(items.length - earned), 'в работе'),
      summaryTile(percentText(Math.round((earned / items.length) * 100)), 'каталога'),
    );

    list.replaceChildren(...items.map((item) => {
      const done = item.value >= item.goal;
      const card = el('article', 'achievement-card' + (done ? ' earned' : ''));
      card.append(el('div', 'achievement-medal', done ? '✓' : `${Math.min(item.value, item.goal)}`));

      const body = el('div');
      const head = el('div', 'achievement-head');
      head.append(el('strong', null, item.title),
        el('span', null, done ? 'получено' : `${Math.min(item.value, item.goal)} из ${item.goal}`));
      const bar = el('div', 'progress');
      const fill = el('i');
      fill.style.width = `${Math.min(100, Math.round((item.value / item.goal) * 100))}%`;
      bar.append(fill);
      body.append(head, el('p', null, item.rule), bar);
      card.append(body);
      return card;
    }));
  }

  function summaryTile(value, label) {
    const tile = el('article');
    tile.append(el('strong', null, value), el('span', null, label));
    return tile;
  }

  // --- profile ----------------------------------------------------------

  function renderProfile(data) {
    const user = data.user || {};
    const summary = data.summary || {};
    setText('#profile-title', user.display_name || '—');
    const initial = (user.display_name || '?').slice(0, 1).toUpperCase();
    setText('#profileAvatar', initial);
    setText('#profilePill', initial);
    setText('#profileSubtitle', summary.predictions
      ? `${summary.predictions} прогнозов в ${summary.tournaments} турнирах`
      : 'Здесь появится ваш профиль после первых прогнозов');

    const kpis = document.querySelector('#profileKpis');
    if (kpis) {
      kpis.replaceChildren(
        kpiTile('ВСЕГО', String(summary.predictions || 0), 'прогнозов'),
        kpiTile('ТОЧНОСТЬ', summary.predictions ? percentText(summary.accuracy) : '—', 'all-time'),
        kpiTile('ОЧКИ', String(summary.points || 0), 'за всё время'),
      );
    }

  }

  function kpiTile(label, value, note) {
    const tile = el('article');
    tile.append(el('small', null, label), el('strong', null, value), el('span', null, note));
    return tile;
  }

  /** emptyLine is the one shape every empty list here uses. */
  function emptyLine(text) {
    return el('p', 'empty-line', text);
  }

  /**
   * failedLine is what a screen shows when its data did not arrive, and it
   * always comes with a way to try again.
   *
   * Every one of these used to be a flat "could not load" and nothing
   * else: a dead end after a dropped connection, a restart mid-deploy, or
   * a phone that lost signal for a second — all of which fix themselves on
   * a retry the screen refused to offer. It also said the same thing when
   * the real answer was "you do not have access yet", which is not a
   * failure and is not fixed by retrying, so that case says so instead.
   */
  function failedLine(text, retry) {
    const wrap = el('div', 'load-failed');
    wrap.append(el('p', 'empty-line', text));
    const again = el('button', 'text-action', 'Попробовать снова');
    again.addEventListener('click', () => {
      wrap.replaceChildren(el('p', 'empty-line', 'Загружаем…'));
      haptic();
      void retry();
    });
    wrap.append(again);
    return wrap;
  }

  /** describeFailure turns an error into something worth reading. */
  function describeFailure(error, subject) {
    if (error instanceof ForbiddenError) {
      return 'Доступ к приложению ещё не выдан — запросите его в боте.';
    }
    if (error instanceof UnauthenticatedError) {
      return 'Откройте приложение из бота: вне Telegram оно не может подтвердить, кто вы.';
    }
    return `Не удалось загрузить ${subject}. Проверьте связь и попробуйте снова.`;
  }

  // --- history ----------------------------------------------------------

  /** chatMedalTotal is every medal won across chats, for the catalogue. */
  let chatMedalTotal = 0;

  /**
   * historyResult is the one narrowing this screen owns: correct or
   * wrong. The discipline and the chat are the app's filter and are not
   * repeated here — the page used to carry its own chips for them right
   * under the bar that already did the same job.
   */
  let historyResult = '';

  async function loadHistory() {
    const feed = document.querySelector('#historyFeed');
    if (!feed) return;
    feed.replaceChildren(emptyLine('Загружаем…'));
    try {
      const body = await fetchJSON(`/api/miniapp/v1/me/history${scopeQuery({ result: historyResult })}`, { signed: true });
      renderHistory(body.entries || []);
      // The dashboard's "latest" strip is this same feed under the same
      // filter, so it is filled here rather than fetched a second time.
      // Only when no result narrowing is on: "your last five" must not
      // quietly become "your last five correct ones".
      if (!historyResult) {
        recentEntries = body.entries || [];
        renderRecent();
        renderForm(recentEntries);
      }
    } catch (error) {
      if (error instanceof ForbiddenError) showAccessNotice('forbidden');
      else if (error instanceof UnauthenticatedError) showAccessNotice('unauthenticated');
      feed.replaceChildren(failedLine(describeFailure(error, 'историю'), loadHistory));
    }
  }

  function renderHistory(entries) {
    const feed = document.querySelector('#historyFeed');
    if (!feed) return;
    if (entries.length === 0) {
      feed.replaceChildren(emptyLine('Здесь появятся ваши прогнозы после первых завершённых матчей.'));
      return;
    }
    const nodes = [];
    let lastDay = '';
    for (const entry of entries) {
      const day = new Date(entry.played_at).toLocaleDateString('ru-RU', { day: '2-digit', month: 'short' });
      if (day !== lastDay) {
        nodes.push(el('div', 'date-label', day.toUpperCase()));
        lastDay = day;
      }
      nodes.push(historyCard(entry));
    }
    feed.replaceChildren(...nodes);
  }

  function historyCard(entry) {
    const result = resultOf(entry);
    const card = el('article', `history-card ${entry.correct ? 'correct' : 'wrong'}`);
    const status = el('div', 'history-status');
    const badgeClass = { exact: 'exact', correct: 'success', wrong: 'danger' }[result];
    status.append(
      el('span', 'game-mini', gameLabel(entry.game)),
      el('span', `result-badge ${badgeClass}`, RESULT_TEXT[result]),
      el('small', null, entry.points > 0 ? `+${entry.points}` : '0'),
    );

    // first_team/second_team, exactly as the API names them: reading
    // entry.first here is how this screen rendered blank team names.
    const matchup = el('div', 'matchup');
    const game = entry.game.toLowerCase();
    const left = el('div', 'team-side');
    left.append(teamMark(game, entry.first_team), el('strong', null, entry.first_team));
    const versus = el('div', 'versus');
    versus.append(el('b', null, entry.actual), el('span', null, `прогноз ${entry.predicted}`));
    const right = el('div', 'team-side right');
    right.append(teamMark(game, entry.second_team), el('strong', null, entry.second_team));
    matchup.append(left, versus, right);

    const foot = el('div', 'history-foot');
    const where = entry.chats > 1 ? `${entry.chat} и ещё ${entry.chats - 1}` : entry.chat || '';
    foot.append(el('span', null, entry.event || ''), el('span', null, where));
    card.append(status, matchup, foot);
    return card;
  }

  /**
   * renderFormStrip draws the last results as a row of marks — the form
   * guide a sports page uses, and the one place on this screen where the
   * recent run is visible as a shape rather than a number.
   */
  function renderFormStrip(recent) {
    const strip = document.querySelector('#formStrip');
    if (!strip) return;
    strip.replaceChildren(...recent.map((won) => {
      const mark = el('i', won ? 'form-mark won' : 'form-mark lost');
      mark.setAttribute('aria-label', won ? 'верно' : 'ошибка');
      return mark;
    }));
  }

  /**
   * renderFilterBar draws the app's one filter: the disciplines this
   * person actually predicts in, and the chats they play in. Both live
   * here and nowhere else — a screen that grows its own copy of either is
   * how "CS2" started meaning two different things.
   *
   * A row with fewer than two options is not a choice, so it is not
   * rendered: one discipline filtered to itself is furniture.
   */
  function renderFilterBar(games, chats) {
    renderGameRail(games);
    renderChatRail(chats);
    const wrap = document.querySelector('#filterBar');
    if (!wrap) return;
    const useful = games.length > 1 || chats.length > 1;
    // Shown once, and then left alone. Every reload of the dashboard used
    // to hide the bar and show it again, and changing a filter reloads the
    // dashboard — so using the filter made the bar blink and the whole
    // page jump under the finger that was still on it.
    if (useful) {
      wrap.hidden = false;
      wrap.dataset.settled = 'true';
    } else if (wrap.dataset.settled !== 'true') {
      wrap.hidden = true;
    }
  }

  /**
   * knownGames is every discipline this person has been seen to play.
   *
   * The rail is built from the current scope, and picking a chat narrows
   * that scope — so a chat that follows one game would collapse the rail
   * to nothing and the whole page would jump up under the finger that had
   * just tapped. The rail is a control, and a control that disappears
   * because you used it is worse than one that shows an option leading
   * nowhere.
   */
  const knownGames = new Map();

  function renderGameRail(games) {
    const rail = document.querySelector('#gameRail');
    const row = document.querySelector('#gameRailRow');
    if (!rail || !row) return;

    // Remembered by code, refreshed by the current scope: a game keeps its
    // place on the rail, and its percentage is whatever the current filter
    // makes it — or absent, when this chat has no predictions in it.
    for (const game of games) knownGames.set(game.game, game);
    const current = new Map(games.map((g) => [g.game, g]));

    if (knownGames.size < 2) {
      row.hidden = true;
      rail.replaceChildren();
      scope.game = '';
      currentGame = games[0]?.game.toLowerCase() || '';
      if (currentGame) void loadLogos(currentGame);
      return;
    }
    row.hidden = false;

    const options = [{ code: '', label: 'Все игры', accuracy: null },
      ...[...knownGames.keys()].map((code) => ({
        code: code.toLowerCase(),
        label: gameLabel(code),
        // Null rather than zero: "no predictions here" and "0% here" are
        // different statements and only one of them is true.
        accuracy: current.has(code) ? current.get(code).accuracy : null,
      }))];
    rail.replaceChildren(...options.map((entry) => {
      const chip = el('button', 'game-chip' + (entry.code === scope.game ? ' active' : ''));
      chip.dataset.game = entry.code;
      chip.setAttribute('aria-pressed', String(entry.code === scope.game));
      chip.append(el('i', 'game-dot'), el('span', null, entry.label));
      if (entry.accuracy !== null) chip.append(el('small', null, percentText(entry.accuracy)));
      chip.addEventListener('click', () => setGame(entry.code));
      return chip;
    }));
    // Every game's crests, not just the selected one: the feeds below mix
    // disciplines, and a row from another game would come out bare.
    for (const code of knownGames.keys()) void loadLogos(code.toLowerCase());
  }

  function renderChatRail(chats) {
    const rail = document.querySelector('#chatRail');
    const row = document.querySelector('#chatRailRow');
    if (!rail || !row) return;
    if (chats.length < 2) {
      row.hidden = true;
      rail.replaceChildren();
      scope.chat = null;
      return;
    }
    row.hidden = false;

    const options = [{ id: null, label: 'Все чаты' },
      ...chats.map((c) => ({ id: c.id, label: c.title, count: c.predictions }))];
    rail.replaceChildren(...options.map((entry) => {
      const active = entry.id === scope.chat;
      const chip = el('button', 'chat-chip' + (active ? ' active' : ''));
      chip.setAttribute('aria-pressed', String(active));
      chip.append(el('span', null, entry.label));
      if (entry.count) chip.append(el('small', null, String(entry.count)));
      chip.addEventListener('click', () => setChat(entry.id));
      return chip;
    }));
  }

  /** scopeIsNarrowed says whether an empty screen is empty because of the
   * filter — which is a different sentence from "you have no history". */
  function scopeIsNarrowed() {
    return Boolean(scope.game) || scope.chat !== null;
  }

  /** showAccessNotice replaces the screen with why it is empty. */
  function showAccessNotice(kind) {
    const notice = document.querySelector('#accessNotice');
    const shell = document.querySelector('#app');
    if (!notice || !shell) return;
    const messages = {
      forbidden: 'Доступ к приложению ещё не выдан. Запросите его в личном кабинете бота — придёт ответ.',
      unauthenticated: 'Откройте приложение из бота: вне Telegram оно не может подтвердить, кто вы.',
      failed: 'Не удалось загрузить данные. Попробуйте открыть приложение ещё раз.',
    };
    notice.textContent = messages[kind] || messages.failed;
    notice.hidden = false;
    shell.dataset.state = kind;
  }

  async function loadDashboard() {
    try {
      const data = await fetchJSON(`/api/miniapp/v1/me/dashboard${scopeQuery()}`, { signed: true });
      applyDashboard(data);
      renderFilterBar((data.games || []).filter((g) => g.predictions > 0), data.chats || []);
      void loadHistory();
      void loadActive();
      void loadChats();
      void loadSettings();
      const notice = document.querySelector('#accessNotice');
      if (notice) notice.hidden = true;
    } catch (error) {
      if (error instanceof ForbiddenError) showAccessNotice('forbidden');
      else if (error instanceof UnauthenticatedError) showAccessNotice('unauthenticated');
      else {
        console.warn('dashboard unavailable', error);
        showAccessNotice('failed');
      }
    }
  }

  /**
   * setGame and setChat change the one filter and reload everything
   * through it. Reloading rather than re-slicing in the browser: the
   * totals, the streaks and the team tables are all computed over the
   * filtered set, and recomputing half of them here is how two screens
   * start disagreeing.
   */
  async function setGame(game) {
    if (scope.game === game) return;
    scope.game = game;
    currentGame = game || currentGame;
    document.documentElement.dataset.game = game || 'all';
    haptic();
    await refresh();
  }

  async function setChat(chatID) {
    if (scope.chat === chatID) return;
    scope.chat = chatID;
    haptic();
    await refresh();
  }

  /** refresh reloads every screen under the current filter. */
  async function refresh() {
    await loadDashboard();
  }


  // --- settings ---------------------------------------------------------
  //
  // The same settings the bot offers in a private conversation. Two
  // owners, never mixed: what a person sets for themselves, and what a
  // manager sets for one chat. Each control writes immediately — a
  // settings screen with a Save button is a screen you can leave in a
  // state you did not save — and rolls back visibly if the write fails.

  const NOTIFY_LABELS = {
    recaps: 'Итоги матчей',
    event_recaps: 'Итоги турниров',
    reminders: 'Напоминания о голосовании',
    new_events: 'Новые турниры',
    event_eve: 'Турнир стартует завтра',
    event_finished: 'Итоги турнира',
    digests: 'Итоги месяца и года',
    streams: 'Ссылка на трансляцию',
  };

  const GAME_TOGGLE_LABELS = { CS2: 'CS2', DOTA2: 'Dota 2' };

  let settings = null;

  async function loadSettings() {
    const root = document.querySelector('#personalSettings');
    if (!root) return;
    try {
      settings = await fetchJSON('/api/miniapp/v1/me/settings', { signed: true });
      renderSettings(settings);
    } catch (error) {
      if (error instanceof ForbiddenError) showAccessNotice('forbidden');
      else if (error instanceof UnauthenticatedError) showAccessNotice('unauthenticated');
      root.replaceChildren(failedLine(describeFailure(error, 'настройки'), loadSettings));
    }
  }

  function renderSettings(data) {
    const personal = document.querySelector('#personalSettings');
    if (personal) {
      personal.replaceChildren(
        choiceRow('Язык', data.personal.locale === 'EN' ? 'English' : 'Русский',
          () => patchPersonal({ locale: data.personal.locale === 'EN' ? 'RU' : 'EN' })),
        // HLTV ранжирует только Counter-Strike, поэтому его картинки есть
        // для CS2 и больше ни для чего: подпись говорит об этом прямо,
        // вместо настройки, которая делает вид, что влияет на всё.
        choiceRow('Логотипы команд (CS2)', data.personal.logo_source === 'hltv' ? 'HLTV' : 'провайдер',
          () => patchPersonal({ logo_source: data.personal.logo_source === 'hltv' ? 'provider' : 'hltv' }),
          'Для остальных игр всегда используется провайдер матчей'),
        choiceRow('Часовой пояс', data.personal.timezone || 'как в чате', null,
          'Меняется в боте: /timezone Area/City'),
        choiceRow('Имя в списках', data.personal.nickname || 'из Telegram', null,
          'Меняется в боте: Настройки → Моё имя в списках'),
        ...data.personal.notify.map((entry) => switchRow(entry, (on) =>
          patchPersonal({ notify: { kind: entry.kind, on } }))),
      );
    }

    const block = document.querySelector('#chatSettingsBlock');
    const chats = document.querySelector('#chatSettings');
    const heading = document.querySelector('#chatSettingsHeading');
    if (!block || !chats) return;
    // Nothing to manage is not an empty section: it is a section that does
    // not belong on this person's screen at all.
    if (!data.chats.length) {
      block.hidden = true;
      return;
    }
    block.hidden = false;

    // The bar at the top narrows this screen too. Somebody who has picked
    // one chat is looking at that chat everywhere else in the app, and a
    // settings list that keeps showing all four is the one place where a
    // tap lands somewhere other than where they are looking.
    const shown = scope.chat === null ? data.chats : data.chats.filter((c) => c.id === scope.chat);
    if (heading) {
      heading.textContent = scope.chat === null
        ? 'ЧАТЫ, КОТОРЫМИ ВЫ УПРАВЛЯЕТЕ'
        : 'ВЫБРАННЫЙ ЧАТ';
    }

    // Picked a chat they play in but do not manage: say so, rather than
    // showing an empty block that reads as a loading failure.
    if (shown.length === 0) {
      chats.replaceChildren(emptyLine('В выбранном чате у вас нет прав на настройки. Снимите фильтр наверху, чтобы увидеть остальные.'));
      return;
    }
    chats.replaceChildren(...shown.map(chatSettingsCard));
  }

  function chatSettingsCard(chat) {
    const card = el('article', 'settings-card');
    card.append(el('h3', null, chat.title || 'Без названия'));

    const patch = (body) => patchChat(chat.id, body);
    card.append(
      choiceRow('Язык', chat.locale === 'EN' ? 'English' : 'Русский',
        () => patch({ locale: chat.locale === 'EN' ? 'RU' : 'EN' })),
      choiceRow('Язык трансляций', chat.stream_language === 'EN' ? 'English' : 'Русский',
        () => patch({ stream_language: chat.stream_language === 'EN' ? 'RU' : 'EN' })),
      ...(hasCS2(chat) ? [choiceRow('Флаги команд (CS2)', chat.prefer_hltv_flags ? 'HLTV' : 'провайдер',
        () => patch({ prefer_hltv_flags: !chat.prefer_hltv_flags }),
        'Для остальных игр всегда используется провайдер матчей')] : []),
      choiceRow('Турниры по умолчанию', chat.top_tier_only ? 'только топ' : 'все',
        () => patch({ top_tier_only: !chat.top_tier_only })),
      choiceRow('Тихие часы', quietText(chat), null, 'Меняются в боте: Настройки → Тихие часы'),
      choiceRow('Часовой пояс', chat.timezone, null, 'Меняется в боте: Настройки → Часовой пояс'),
    );

    for (const game of chat.games) {
      const label = GAME_TOGGLE_LABELS[game.game] || game.game;
      card.append(switchRow({ kind: game.game, on: game.enabled }, (on) =>
        patch({ game: { game: game.game, enabled: on } }), label));
      // Auto-subscribing to a game the chat does not follow is a setting
      // with nothing to act on, so it only appears once the game is on.
      if (game.enabled) {
        card.append(switchRow({ kind: game.game + ':auto', on: game.auto_subscribe }, (on) =>
          patch({ game: { game: game.game, auto_subscribe: on } }), label + ': добавлять топ-турниры сразу'));
      }
    }
    for (const entry of chat.notify) {
      card.append(switchRow(entry, (on) => patch({ notify: { kind: entry.kind, on } })));
    }
    return card;
  }

  /** hasCS2 reports whether a chat follows Counter-Strike, which is the
   * only game HLTV has an answer for. */
  function hasCS2(chat) {
    return (chat.games || []).some((g) => g.game === 'CS2' && g.enabled);
  }

  function quietText(chat) {
    if (chat.quiet_from < 0 || chat.quiet_to < 0) return 'не заданы';
    const hhmm = (m) => String(Math.floor(m / 60)).padStart(2, '0') + ':' + String(m % 60).padStart(2, '0');
    return hhmm(chat.quiet_from) + ' — ' + hhmm(chat.quiet_to);
  }

  /** choiceRow is a setting with a value. Without onTap it is read-only and
   * says where it is changed instead of looking broken. */
  function choiceRow(label, value, onTap, hint) {
    const row = el(onTap ? 'button' : 'div', 'setting-row' + (onTap ? '' : ' is-static'));
    const copy = el('div');
    copy.append(el('strong', null, label));
    if (hint) copy.append(el('small', null, hint));
    row.append(copy, el('span', 'setting-value', value));
    if (onTap) {
      row.addEventListener('click', () => {
        haptic();
        void onTap();
      });
    }
    return row;
  }

  /** switchRow is a boolean. It flips on screen at once and flips back if
   * the write fails: a control that lies about what was saved is worse
   * than a slow one. */
  function switchRow(entry, write, labelOverride) {
    const row = el('button', 'setting-row setting-toggle' + (entry.on ? ' is-on' : ''));
    row.setAttribute('aria-pressed', String(entry.on));
    row.append(el('strong', null, labelOverride || NOTIFY_LABELS[entry.kind] || entry.kind));
    row.append(el('i', 'setting-switch'));
    row.addEventListener('click', async () => {
      const next = !row.classList.contains('is-on');
      row.classList.toggle('is-on', next);
      row.setAttribute('aria-pressed', String(next));
      haptic();
      try {
        await write(next);
      } catch (_) {
        row.classList.toggle('is-on', !next);
        row.setAttribute('aria-pressed', String(!next));
      }
    });
    return row;
  }

  async function patchPersonal(body) {
    settings = await fetchJSON('/api/miniapp/v1/me/settings', { signed: true, method: 'PATCH', body });
    renderSettings(settings);
    // The crest source is one of these, and it decides which pictures the
    // rest of the app asks for.
    if (settings.personal.logo_source && settings.personal.logo_source !== logoSource) {
      logoSource = settings.personal.logo_source;
      logos.clear();
      await refresh();
    }
  }

  async function patchChat(chatID, body) {
    const updated = await fetchJSON(`/api/miniapp/v1/chats/${chatID}/settings`, { signed: true, method: 'PATCH', body });
    settings.chats = settings.chats.map((c) => (c.id === chatID ? updated : c));
    renderSettings(settings);
  }

  // --- navigation -----------------------------------------------------

  const screens = [...document.querySelectorAll('.screen')];
  const navButtons = [...document.querySelectorAll('.bottom-nav button')];

  /**
   * screenStack remembers how somebody got here, one step at a time.
   *
   * Telegram's back button used to go straight to the overview from
   * anywhere, so Profile → Settings → Back skipped the screen it was
   * opened from. A back button that lands somewhere you were not is worse
   * than none: it teaches people not to trust it.
   *
   * Only the tabs reset it — tapping a tab is starting somewhere, not
   * going deeper.
   */
  const screenStack = [];

  function showScreen(name, { deeper = false } = {}) {
    const current = screens.find((screen) => screen.classList.contains('is-active'))?.dataset.screen;
    if (deeper && current && current !== name) screenStack.push(current);
    else if (!deeper) screenStack.length = 0;
    for (const screen of screens) screen.classList.toggle('is-active', screen.dataset.screen === name);
    for (const button of navButtons) {
      const active = button.dataset.target === name;
      button.classList.toggle('active', active);
      if (active) button.setAttribute('aria-current', 'page');
      else button.removeAttribute('aria-current');
    }
    const reduceMotion = matchMedia('(prefers-reduced-motion: reduce)').matches;
    window.scrollTo({ top: 0, behavior: reduceMotion ? 'auto' : 'smooth' });
    haptic();
    syncBackButton(name);
  }

  /**
   * Telegram's own back button, rather than a second one drawn in the
   * page: on the home screen it is hidden, everywhere else it returns
   * there, which is what a Mini App user expects it to do.
   */
  function syncBackButton(name) {
    const back = tg?.BackButton;
    if (!back) return;
    try {
      if (name === 'dashboard' && screenStack.length === 0) back.hide();
      else back.show();
    } catch (_) {
      /* older clients have no BackButton */
    }
  }

  // --- filters and sheet ----------------------------------------------

  function initFilters() {
    for (const chip of document.querySelectorAll('.filter-rail .chip')) {
      chip.addEventListener('click', () => {
        for (const other of document.querySelectorAll('.filter-rail .chip')) other.classList.remove('selected');
        chip.classList.add('selected');
      });
    }
  }

  function initSheet() {
    const backdrop = document.querySelector('#sheetBackdrop');
    if (!backdrop) return;
    const open = () => {
      backdrop.hidden = false;
      document.body.style.overflow = 'hidden';
      haptic('impact');
    };
    const close = () => {
      backdrop.hidden = true;
      document.body.style.overflow = '';
    };
    document.querySelector('#filterButton')?.addEventListener('click', open);
    document.querySelector('#closeSheet')?.addEventListener('click', close);
    document.querySelector('#applyFilters')?.addEventListener('click', () => {
      historyResult = document.querySelector('#filterResult')?.value || '';
      void loadHistory();
      close();
    });
    backdrop.addEventListener('click', (event) => {
      if (event.target === backdrop) close();
    });
    // Escape closes it too: the sheet is a dialog, and a dialog that can
    // only be dismissed by tapping exactly the right pixel is a trap.
    document.addEventListener('keydown', (event) => {
      if (event.key === 'Escape' && !backdrop.hidden) close();
    });
  }

  // --- boot -----------------------------------------------------------

  function syncTheme() {
    document.documentElement.dataset.telegramScheme = tg?.colorScheme || 'dark';
  }

  function init() {
    initTelegram();
    syncTheme();
    try {
      tg?.onEvent?.('themeChanged', syncTheme);
    } catch (_) {
      /* nothing to sync on a plain browser */
    }
    try {
      // One step back, to wherever this screen was opened from.
      tg?.BackButton?.onClick?.(() => showScreen(screenStack.pop() || 'dashboard'));
    } catch (_) {
      /* older clients have no BackButton */
    }

    for (const button of navButtons) button.addEventListener('click', () => showScreen(button.dataset.target));
    // A link into another screen goes deeper; a tab does not.
    for (const button of document.querySelectorAll('[data-go]')) {
      button.addEventListener('click', () => showScreen(button.dataset.go, { deeper: true }));
    }
    initFilters();
    initSheet();

    void loadDashboard();
    const screen = params.get('screen');
    if (screen) showScreen(screen);
    else syncBackButton('dashboard');
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
