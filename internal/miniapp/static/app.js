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

  const RESULT_TEXT = { correct: '✓ Верно', wrong: '× Ошибка' };

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
    if (logos.has(game)) return;
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

  function logoFor(game, teamName) {
    return logos.get(game)?.get(teamName.toLowerCase()) || '';
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
      el('span', null, entry.correct ? RESULT_TEXT.correct : RESULT_TEXT.wrong));
    row.append(main, result);
    return row;
  }

  const setText = (selector, value) => {
    const node = document.querySelector(selector);
    if (node) node.textContent = value;
  };

  // --- the real numbers ------------------------------------------------
  //
  // Everything above is the prototype's own demo data. What follows is the
  // person's actual record, read from the bot's database through a signed
  // launch. When it arrives it replaces the demo figures; when it does not
  // — no access yet, no network — the screen says so rather than leaving
  // mock numbers on display as if they were real.

  let dashboard = null;
  /** currentGame is the rail's selection, used when looking up crests. */
  let currentGame = 'cs2';

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
    if (data.form?.recent?.length) {
      const wins = data.form.recent.filter(Boolean).length;
      setText('#formScore', Math.round((wins / data.form.recent.length) * 100));
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

  function renderAnalytics(data) {
    const games = [...(data.games || [])].filter((g) => g.predictions > 0);
    setText('#analyticsGameLabel', games.length ? games.map((g) => gameLabel(g.game)).join(' · ') : '—');
    setText('#analyticsGameCount', games.length || '—');
    const total = games.reduce((sum, g) => sum + g.predictions, 0);
    setText('#analyticsCoverage', total ? `${total} прогнозов всего` : 'нет данных');

    // Best and worst are only worth naming where the sample can support
    // the claim; below that a percentage is a coin toss with a label.
    const ranked = games.filter((g) => g.predictions >= MIN_SEGMENT_SAMPLE)
      .sort((a, b) => b.accuracy - a.accuracy);
    const best = ranked[0];
    const worst = ranked.length > 1 ? ranked[ranked.length - 1] : null;
    setText('#analyticsBest', best ? gameLabel(best.game) : '—');
    setText('#analyticsBestNote', best ? `${percentText(best.accuracy)} на ${best.predictions}` : `нужно ${MIN_SEGMENT_SAMPLE}+ прогнозов`);
    setText('#analyticsWorst', worst ? gameLabel(worst.game) : '—');
    setText('#analyticsWorstNote', worst ? `${percentText(worst.accuracy)} на ${worst.predictions}` : 'пока не с чем сравнивать');

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

    const teams = document.querySelector('#analyticsTeams');
    if (teams) {
      teams.replaceChildren(...(data.teams || []).map((team, index) => {
        const row = el('div', 'team-row' + (team.accuracy < 50 ? ' low' : ''));
        row.append(el('span', 'rank-num', String(index + 1).padStart(2, '0')), teamMark(currentGame, team.team));
        const copy = el('div', 'team-copy');
        copy.append(el('strong', null, team.team), el('small', null, `${team.predictions} прогнозов`));
        row.append(copy, el('em', null, percentText(team.accuracy)));
        return row;
      }));
      if (!(data.teams || []).length) {
        teams.replaceChildren(emptyLine('Команда появится здесь, когда прогнозов по ней станет достаточно.'));
      }
    }
  }

  // --- achievements -----------------------------------------------------
  //
  // Derived from the same figures, with the rule written on each card.
  // Nothing is invented: a badge is a threshold over a number the database
  // actually holds, and its progress is that number.

  function achievementsFor(data) {
    const summary = data.summary || {};
    const form = data.form || {};
    const strongGames = (data.games || []).filter((g) => g.predictions >= 25 && g.accuracy >= 65).length;
    return [
      { title: 'Сотня', rule: '100 верных прогнозов', value: summary.correct || 0, goal: 100 },
      { title: 'Снайпер', rule: '25 точных счётов', value: summary.exact || 0, goal: 25 },
      { title: 'Серия', rule: '5 верных подряд', value: form.best_streak || 0, goal: 5 },
      { title: 'Мультигейм', rule: '≥65% в двух играх при n≥25', value: strongGames, goal: 2 },
      { title: 'Турнирный стаж', rule: '10 турниров с прогнозами', value: summary.tournaments || 0, goal: 10 },
    ];
  }

  function renderAchievements(data) {
    const list = achievementsFor(data);
    const earned = list.filter((a) => a.value >= a.goal).length;

    const summary = document.querySelector('#achievementSummary');
    if (summary) {
      summary.replaceChildren(
        summaryTile(String(earned), 'получено'),
        summaryTile(String(list.length - earned), 'в прогрессе'),
        summaryTile(String((data.games || []).length), 'дисциплины'),
      );
    }

    const root = document.querySelector('#achievementList');
    if (!root) return;
    root.replaceChildren(...list.map((item) => {
      const done = item.value >= item.goal;
      const card = el('article', 'achievement-card' + (done ? ' earned' : ''));
      card.append(el('div', 'achievement-medal', done ? '✓' : String(item.goal)));
      const body = el('div');
      const head = el('div', 'achievement-head');
      head.append(el('strong', null, item.title), el('span', null, done ? 'Получено' : 'В прогрессе'));
      const bar = el('div', 'progress');
      const fill = el('i');
      fill.style.width = `${Math.min(100, Math.round((item.value / item.goal) * 100))}%`;
      bar.append(fill);
      body.append(head, el('p', null, item.rule), bar, el('small', null, `${item.value} / ${item.goal}`));
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
    setText('#profileAvatar', (user.display_name || '?').slice(0, 1).toUpperCase());
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

  let historyGame = '';
  let historyResult = '';

  async function loadHistory() {
    const feed = document.querySelector('#historyFeed');
    if (!feed) return;
    feed.replaceChildren(emptyLine('Загружаем…'));
    try {
      const query = new URLSearchParams();
      if (historyGame) query.set('game', historyGame);
      if (historyResult) query.set('result', historyResult);
      const suffix = query.toString() ? `?${query}` : '';
      const body = await fetchJSON(`/api/miniapp/v1/me/history${suffix}`, { signed: true });
      renderHistory(body.entries || []);
      // The dashboard's "latest" strip is the same feed, unfiltered — so
      // it is filled here rather than fetched a second time.
      if (!historyGame && !historyResult) {
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
    const card = el('article', `history-card ${entry.correct ? 'correct' : 'wrong'}`);
    const status = el('div', 'history-status');
    status.append(
      el('span', 'game-mini', gameLabel(entry.game)),
      el('span', `result-badge ${entry.correct ? 'success' : 'danger'}`, entry.correct ? '✓ Верно' : '× Ошибка'),
      el('small', null, entry.points > 0 ? `+${entry.points}` : '0'),
    );

    const matchup = el('div', 'matchup');
    const left = el('div', 'team-side');
    left.append(teamMark(entry.game.toLowerCase(), entry.first), el('strong', null, entry.first));
    const versus = el('div', 'versus');
    versus.append(el('b', null, entry.actual), el('span', null, `прогноз ${entry.predicted}`));
    const right = el('div', 'team-side right');
    right.append(teamMark(entry.game.toLowerCase(), entry.second), el('strong', null, entry.second));
    matchup.append(left, versus, right);

    const foot = el('div', 'history-foot');
    const where = entry.chats > 1 ? `${entry.chat} и ещё ${entry.chats - 1}` : entry.chat || '';
    foot.append(el('span', null, entry.event || ''), el('span', null, where));
    card.append(status, matchup, foot);
    return card;
  }

  /**
   * renderHistoryFilters builds one chip per game the person plays — and
   * fills the sheet's own game select from the same list, so the two
   * controls can never offer different games.
   */
  function renderHistoryFilters(games) {
    const select = document.querySelector('#filterGame');
    if (select) {
      select.replaceChildren(el('option', null, 'Все игры'),
        ...games.map((g) => {
          const option = el('option', null, gameLabel(g.game));
          option.value = g.game;
          return option;
        }));
      select.querySelector('option').value = '';
    }
    const rail = document.querySelector('#historyFilters');
    if (!rail) return;
    const chips = [{ code: '', label: 'Все' }, ...games.map((g) => ({ code: g.game, label: gameLabel(g.game) }))];
    rail.replaceChildren(...chips.map((chip) => {
      const button = el('button', 'chip' + (chip.code === historyGame ? ' selected' : ''), chip.label);
      button.dataset.filterGame = chip.code;
      button.addEventListener('click', () => {
        historyGame = chip.code;
        const select = document.querySelector('#filterGame');
        if (select) select.value = chip.code;
        for (const other of rail.querySelectorAll('.chip')) other.classList.remove('selected');
        button.classList.add('selected');
        void loadHistory();
      });
      return button;
    }));
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
   * renderGameRail replaces the demo rail with the games this person has
   * actually predicted in. A game nobody has played is not a filter, it is
   * a dead end with a label on it.
   */
  function renderGameRail(games) {
    const rail = document.querySelector('#gameRail');
    if (!rail || games.length === 0) return;
    rail.replaceChildren(...games.map((entry) => {
      const chip = el('button', 'game-chip');
      chip.dataset.game = entry.game.toLowerCase();
      chip.append(el('i', 'game-dot'), el('span', null, gameLabel(entry.game)),
        el('small', null, percentText(entry.accuracy)));
      chip.addEventListener('click', () => setGame(chip.dataset.game));
      return chip;
    }));
    void setGame(games[0].game.toLowerCase());
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
      const data = await fetchJSON('/api/miniapp/v1/me/dashboard', { signed: true });
      applyDashboard(data);
      renderHistoryFilters(data.games || []);
      void loadHistory();
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
   * setGame switches which discipline the screens are about. There is no
   * data of its own to load: the dashboard already carries every game's
   * record, and this only changes what is highlighted and which crests are
   * fetched.
   */
  async function setGame(game) {
    currentGame = game;
    document.documentElement.dataset.game = game;
    for (const chip of document.querySelectorAll('.game-chip')) {
      const active = chip.dataset.game === game;
      chip.classList.toggle('active', active);
      chip.setAttribute('aria-pressed', String(active));
    }
    haptic();
    // Crests arrive after the first paint: the board is readable with
    // initials, and re-rendering once they land avoids holding the screen
    // hostage to a network round trip.
    await loadLogos(game);
    renderRecent();
    if (dashboard) renderAnalytics(dashboard);
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
      historyGame = document.querySelector('#filterGame')?.value || '';
      historyResult = document.querySelector('#filterResult')?.value || '';
      // The chips and the sheet are two views of one filter: whichever was
      // touched last, both end up showing the same thing.
      for (const chip of document.querySelectorAll('#historyFilters .chip')) {
        chip.classList.toggle('selected', (chip.dataset.filterGame || '') === historyGame);
      }
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
    for (const chip of document.querySelectorAll('.game-chip')) {
      chip.addEventListener('click', () => setGame(chip.dataset.game));
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
