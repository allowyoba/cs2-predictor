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
  const LOGO_SOURCE = params.get('logos') === 'hltv' ? 'hltv' : 'provider';

  /** logos maps a lowercased team name to its crest URL, per game. */
  const logos = new Map();

  /**
   * fetchJSON is every network read here: bounded, and never throwing past
   * the caller.
   *
   * Personal reads carry Telegram's own signed launch parameters, which is
   * the only credential this app has — there is no token of ours to store,
   * and nothing to leak if the page is opened anywhere else.
   */
  async function fetchJSON(path, { timeoutMs = 6000, signed = false, method = 'GET' } = {}) {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const headers = { Accept: 'application/json' };
    if (signed) {
      const initData = tg?.initData || '';
      if (!initData) throw new UnauthenticatedError();
      headers.Authorization = `tma ${initData}`;
    }
    try {
      const response = await fetch(`${API_BASE}${path}`, { method, signal: controller.signal, headers });
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
      const body = await fetchJSON(`/api/miniapp/v1/teams?game=${encodeURIComponent(game)}&logos=${LOGO_SOURCE}`);
      const byName = logos.get(game);
      for (const team of body.teams || []) {
        if (team.name && team.logo) byName.set(team.name.toLowerCase(), team.logo);
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
  function teamMark(game, name) {
    const wrap = el('span', 'team-logo');
    const initials = el('b', null, initialsOf(name));
    const url = logoFor(game, name);
    if (url) {
      const img = el('img');
      img.src = url;
      img.alt = '';
      img.loading = 'lazy';
      img.decoding = 'async';
      // A crest that fails to load leaves the initials in place rather
      // than a broken-image glyph.
      img.addEventListener('error', () => img.remove());
      wrap.append(img);
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
      root.replaceChildren(emptyLine('Не удалось загрузить активные прогнозы.'));
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
      root.replaceChildren(emptyLine('Не удалось загрузить статистику по чатам.'));
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
      }
    } catch (error) {
      if (error instanceof ForbiddenError) showAccessNotice('forbidden');
      else if (error instanceof UnauthenticatedError) showAccessNotice('unauthenticated');
      feed.replaceChildren(emptyLine('Не удалось загрузить историю.'));
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
    if (wrap) {
      wrap.hidden = games.length < 2 && chats.length < 2;
    }
  }

  function renderGameRail(games) {
    const rail = document.querySelector('#gameRail');
    const row = document.querySelector('#gameRailRow');
    if (!rail || !row) return;
    if (games.length < 2) {
      row.hidden = true;
      rail.replaceChildren();
      scope.game = '';
      currentGame = games[0]?.game.toLowerCase() || '';
      if (currentGame) void loadLogos(currentGame);
      return;
    }
    row.hidden = false;

    const chips = [{ code: '', label: 'Все игры', accuracy: null },
      ...games.map((g) => ({ code: g.game.toLowerCase(), label: gameLabel(g.game), accuracy: g.accuracy }))];
    rail.replaceChildren(...chips.map((entry) => {
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
    for (const game of games) void loadLogos(game.game.toLowerCase());
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

    const chips = [{ id: null, label: 'Все чаты' },
      ...chats.map((c) => ({ id: c.id, label: c.title, count: c.predictions }))];
    rail.replaceChildren(...chips.map((entry) => {
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

  // --- navigation -----------------------------------------------------

  const screens = [...document.querySelectorAll('.screen')];
  const navButtons = [...document.querySelectorAll('.bottom-nav button')];

  function showScreen(name) {
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
      if (name === 'dashboard') back.hide();
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
      tg?.BackButton?.onClick?.(() => showScreen('dashboard'));
    } catch (_) {
      /* older clients have no BackButton */
    }

    for (const button of navButtons) button.addEventListener('click', () => showScreen(button.dataset.target));
    for (const button of document.querySelectorAll('[data-go]')) {
      button.addEventListener('click', () => showScreen(button.dataset.go));
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
