# Predictor Mini App — Multi-Game Analyst Edition

**Версия:** 2.0  
**Продукт:** Telegram Mini App для Predictor ecosystem; CS2 — первая fully supported дисциплина  
**Статус:** дизайн и продуктовая спецификация для MVP → Analytics → Progression → Coach

## 1. Продуктовая задача

Mini App дополняет бота и превращает уже накопленную историю прогнозов в персональный аналитический профиль. Бот остаётся основным местом, где пользователь делает прогноз; Mini App отвечает на вопросы:

1. Насколько хорошо я прогнозирую сейчас?
2. В каких форматах и командах я сильнее/слабее?
3. Улучшаюсь ли я?
4. Какие ошибки повторяются?
5. Что конкретно стоит изменить в следующей серии прогнозов?

Ключевой принцип: **не показывать метрику, если она не выводится из реально сохранённых данных.**

## 2. Что уже поддерживает текущий backend

По текущей схеме и доменной логике проект уже хранит или вычисляет:

- количество прогнозов пользователя;
- правильные прогнозы и outcome accuracy;
- точные прогнозы счёта;
- очки;
- турниры;
- последние прогнозы с командами, прогнозом счёта, итоговым счётом и результатом;
- текущую и лучшую серию правильных прогнозов;
- recent form за последние прогнозы;
- сравнение последних 30 дней с предыдущими 30 днями;
- accuracy по командам при достаточной выборке;
- формат серии (BO1/BO3/BO5 и др.);
- турнир, стадию, tier турнира;
- медали/турнирные результаты.

Это позволяет сделать содержательный MVP без новой модели данных.

## 3. Что нельзя обещать в MVP без новых данных

Следующие пункты из версии 1.0 переносятся в отдельный data-enrichment этап:

| Идея | Почему не входит в MVP | Что потребуется |
|---|---|---|
| Аналитика по картам (Mirage/Inferno/Ancient) | Карты не сохраняются как отдельные сущности матча/прогноза | ingest map veto/results + схема хранения карт |
| «Вероятность ★★★★☆» | Нет пользовательской или модельной probability/confidence | сохранять confidence пользователя либо probability модели |
| Difficulty Factor | Нет нормализованной вероятности исхода на момент прогноза | pre-match rating/odds/probability snapshot |
| «равные матчи 50/50» | Нельзя достоверно определить равенство без pre-match probability | probability snapshot или формализованный proxy |
| «после замены игрока» | Нет roster-change events, привязанных ко времени матча | roster snapshots + change events |
| LAN vs Online | Надёжный тип проведения не является нормализованным полем текущей модели | нормализовать venue/mode |

UI не должен подменять эти данные эвристиками, выдавая их за факт.

## 4. Информационная архитектура

### Нижняя навигация

1. **Обзор** — текущая форма и главное действие.
2. **Аналитика** — сегменты, команды, форматы, стадии, тренды.
3. **История** — все завершённые прогнозы и фильтры.
4. **Награды** — достижения и прогресс.
5. **Профиль** — агрегаты, месячные отчёты, настройки отображения.

Monthly Report и Coach — не отдельные табы: они являются drill-down из Обзора/Профиля/Аналитики.


## 4.1. Multi-game domain layer

Интерфейс и API не должны кодировать CS2 как единственный домен. Базовые сущности получают `game_id` (`cs2`, `dota2`, `valorant`, `lol`, далее расширяемо). Общие сущности: user, prediction, match, tournament, team, result. Game-specific поля хранятся через нормализованные adapters/metadata, а не добавляются как универсальные nullable columns.

UI contract:

- глобальный GameRail меняет контекст Dashboard/Analytics;
- History умеет `game=all|<game_id>`;
- Profile содержит cross-game portfolio;
- Achievements делятся на `scope=global` и `scope=game`;
- Coach получает `game_id` и не сравнивает несопоставимые игровые сегменты;
- CS2 может показывать официальные/разрешённые team logos, остальные игры используют тот же `TeamIdentity` component;
- game color — presentation token, а не бизнес-логика.

API минимально должен принимать/возвращать `game_id`; агрегаты all-games рассчитываются отдельно от game-specific accuracy. Нельзя смешивать разные определения результата/формата в один сегмент без adapter layer.

## 5. Экран «Обзор»

### Цель

За 5–10 секунд дать пользователю ответ: «как я выступаю сейчас и куда нажать дальше».

### Блоки

**Header**
- имя Telegram;
- аватар при наличии;
- подпись `Analyst profile`;
- период: `Все время` / выбранный период.

**Hero metric**
- outcome accuracy в процентах;
- `correct / total`;
- изменение за последние 30 дней относительно предыдущих 30 дней, только если обе выборки существуют;
- размер выборки рядом с трендом.

**Form strip**
- последние 10 результатов: success/error;
- текущая серия;
- лучшая серия.

**Compact stats**
- очки;
- точные счёты;
- турниры.

**Coach insight**
- один главный actionable insight;
- обязательная подпись выборки: `на основе 18 BO3`;
- CTA `Разобрать аналитику`.

**Recent predictions**
- 3–5 последних прогнозов;
- команды;
- формат;
- прогноз → итог;
- результат;
- турнир/дата вторичным текстом.

**Monthly report teaser**
- accuracy месяца;
- delta к предыдущему месяцу;
- CTA к полному отчёту.

### Изменение относительно ТЗ 1.0

`Prediction Score = Accuracy × Difficulty × Consistency` не используется в MVP, потому что Difficulty Factor сейчас не измеряется. Герой экрана — прозрачная accuracy + trend + sample size. Composite Analyst Score можно добавить позже, когда каждый его компонент будет измерим и документирован.

## 6. Экран «Аналитика»

### Верхний уровень

Период:
- 30 дней;
- 90 дней;
- год;
- всё время;
- конкретный турнир (если API это позволяет).

### Секции MVP

**Форматы серий**
- BO1, BO3, BO5, Fixed Maps / First To при наличии;
- accuracy;
- `correct / predictions`;
- сортировка по выборке, не только по проценту.

**Команды**
- команды с минимальной выборкой `n >= 3`;
- accuracy;
- объём выборки;
- лучший/худший читаемый паттерн показывать только при достаточной выборке.

**Стадии/турниры**
- группировать по нормализованному stage, если строка доступна;
- fallback — по tier турнира (S/A/B/прочее);
- не пытаться автоматически превращать произвольную stage-строку в «Playoff»/«Group» без явной нормализации.

**Динамика**
- последние 30 дней vs предыдущие 30 дней;
- тренд в percentage points, не в относительных процентах.

### Анализ ошибок v1

Coach не объявляет причинность. Он находит повторяемые сегменты:

- сегмент с accuracy заметно ниже общей;
- минимальная выборка для вывода;
- разница в percentage points;
- пример 2–3 последних ошибок внутри сегмента.

Формулировка: `В BO1 точность 52% (n=21), на 18 п.п. ниже вашей общей`.  
Не: `Вы плохо анализируете BO1 из-за импульсивных решений`.

## 7. Экран «История»

### Строка прогноза

- команды;
- турнир;
- дата/время;
- формат;
- `Прогноз 2:1`;
- `Итог 2:0`;
- badge `Верно` / `Ошибка`;
- заработанные очки.

### Фильтры MVP

- период/дата;
- результат;
- команда;
- турнир;
- формат серии.

`Карта` добавляется только после появления map-level данных.

### UX

- sticky filter bar;
- быстрые chips `Все / Верно / Ошибка`;
- пагинация cursor-based или limit/offset с серверной фильтрацией;
- skeleton при загрузке;
- отдельные empty states для `нет истории` и `фильтр ничего не нашёл`.

## 8. Экран «Награды»

Награды должны быть основаны на проверяемых данных и иметь три состояния:

- `locked`;
- `in_progress`;
- `earned`.

Каждая карточка:
- название;
- описание условия;
- прогресс;
- уровень/ступень при наличии;
- дата получения;
- icon + category.

### Набор MVP

**Volume**
- Rookie Analyst — 25 прогнозов;
- Field Analyst — 100;
- Veteran Analyst — 500.

**Accuracy**
- Sniper — ≥ 80% на 25 прогнозах;
- Sharpshooter — ≥ 85% на 50;
- Oracle — ≥ 90% на 100.

**Streak**
- Hot Hand — 5 подряд;
- Unstoppable — 10;
- Legendary Run — 20.

**Exact score**
- Detail Oriented — 10 точных счётов;
- Score Reader — 50;
- Exact Science — 100.

Достижения `Underdog Hunter`, `Coin Flip Master` и аналоги зависят от pre-match difficulty/probability и идут после data-enrichment.

## 9. Экран «Профиль»

- Telegram identity;
- total predictions;
- correct;
- accuracy;
- exact scores;
- points;
- tournaments;
- best streak;
- блок месячных отчётов;
- privacy/help links;
- версия Mini App.

Не дублировать Dashboard целиком: Profile — долгосрочная статистика и архив отчётов.

## 10. Monthly Report

### Содержание

- месяц;
- predictions / correct / accuracy;
- точные счёты;
- points;
- тренд к предыдущему месяцу в п.п.;
- лучший формат при `n >= 5`;
- самая часто прогнозируемая команда;
- лучшая серия месяца;
- 1–2 проверяемых Coach insights.

### Недостаток данных

Если предыдущий месяц пуст — не показывать `0% → 75%` как рост. Вместо этого: `Первый полный месяц статистики`.

## 11. AI Coach

### Coach v1 — deterministic/rules-based

Для первого релиза лучше не начинать с генеративной модели. Backend формирует структурированные insights по правилам, а UI только объясняет их человеческим языком.

Пример payload:

```json
{
  "type": "segment_gap",
  "segment": "BO1",
  "segment_accuracy": 52,
  "overall_accuracy": 70,
  "delta_pp": -18,
  "sample": 21,
  "confidence": "medium",
  "recommendation_key": "review_bo1_recent_losses"
}
```

Преимущества:
- воспроизводимость;
- отсутствие галлюцинаций;
- легко тестировать;
- объяснимый размер выборки;
- позже генеративный слой можно использовать только для wording поверх структурированных фактов.

## 12. Telegram Mini App UX и platform requirements

- mobile-first, базовый макет 390 px;
- поддерживать Telegram theme params и theme change event;
- учитывать `safeAreaInset` и `contentSafeAreaInset`;
- нижний navigation bar размещать относительно content safe area, а не `100vh`;
- вызывать `ready()` после инициализации UI;
- использовать `viewportStableHeight` для стабильной высоты;
- использовать Telegram BackButton на внутренних drill-down экранах;
- haptics только на значимых действиях, без постоянной вибрации;
- tap targets минимум 44×44 px;
- не полагаться на hover;
- `prefers-reduced-motion` для анимаций;
- любой цветовой сигнал дублировать текстом/icon, не кодировать успех только зелёным.

## 13. Авторизация и безопасность

### Flow

1. Mini App получает `Telegram.WebApp.initData`.
2. Frontend передаёт raw initData backend’у, например в `Authorization: tma <initData>`.
3. Backend валидирует подпись Telegram.
4. Backend проверяет `auth_date` на допустимую давность.
5. Backend использует только валидированный Telegram user id.
6. `initDataUnsafe` не является источником доверенных данных.

### Дополнительно

- не принимать `user_id` из query/body как авторизацию;
- rate limit для Mini App API отдельно от webhook;
- короткий cache-control для персональных endpoint’ов или `no-store`;
- CSP и запрет framing вне Telegram/web-клиента при необходимости;
- не отправлять PandaScore/Telegram secrets во frontend.

## 14. API v2

Рекомендуется не строить экран из пяти последовательных запросов. Для mobile UX нужен агрегированный dashboard endpoint.

### `GET /api/miniapp/v1/dashboard`

```json
{
  "user": {
    "display_name": "Alex",
    "photo_url": null
  },
  "summary": {
    "predictions": 542,
    "correct": 401,
    "accuracy": 74,
    "exact": 86,
    "points": 612,
    "tournaments": 32
  },
  "form": {
    "current_streak": 4,
    "best_streak": 11,
    "recent": [true, true, false, true, true, true, true]
  },
  "trend_30d": {
    "current_accuracy": 75,
    "previous_accuracy": 67,
    "delta_pp": 8,
    "current_sample": 84,
    "previous_sample": 63
  },
  "recent_predictions": [],
  "coach": null
}
```

### `GET /api/miniapp/v1/analytics?period=90d`

Возвращает:
- summary;
- by_series_format;
- by_team;
- by_event_tier;
- by_stage при наличии нормализуемых данных;
- trend.

### `GET /api/miniapp/v1/history`

Query:
- `cursor`;
- `limit`;
- `result=correct|wrong`;
- `team_id`;
- `event_id`;
- `series=BO1|BO3|BO5`;
- `from` / `to`.

### `GET /api/miniapp/v1/achievements`

### `GET /api/miniapp/v1/reports/monthly?year=2026&month=9`

### `GET /api/miniapp/v1/coach?period=90d`

Все endpoint’ы user-scoped исключительно по Telegram initData.

## 15. Performance

Цели для production Mini App:

- first usable UI ≤ 2.5 s на среднем мобильном соединении;
- dashboard API p95 ≤ 400 ms внутри инфраструктуры при прогретой БД;
- payload Dashboard желательно ≤ 50 KB;
- не грузить chart library на Dashboard, если графики открываются только в Analytics;
- lazy load тяжёлых экранов/визуализаций;
- skeletons вместо блокирующего spinner на весь экран.

## 16. Accessibility

- WCAG AA по контрасту текста;
- основной текст ≥ 14 px, предпочтительно 15–16 px;
- вторичный текст не ниже 12 px;
- фокус-стили для web/desktop Telegram;
- aria-label для icon-only controls;
- `aria-current` для bottom nav;
- числа не должны зависеть только от цветовой шкалы.

## 17. Этапы

### Этап A — Foundation + Dashboard

- Telegram shell;
- initData auth;
- theme/safe areas;
- dashboard aggregate endpoint;
- summary, form, trend, recent predictions;
- error/empty/loading states.

### Этап B — Analytics + History

- formats/teams/tournament tier;
- filterable history;
- basic error-pattern engine.

### Этап C — Achievements + Reports

- deterministic achievements;
- monthly report;
- archive.

### Этап D — Coach v1

- rule engine;
- confidence/sample thresholds;
- explainable recommendations.

### Этап E — Enriched analytics

После добавления новых данных:
- maps;
- pre-match difficulty/probability;
- underdog/coin-flip analysis;
- roster-change context;
- LAN/Online.

## 18. Definition of Done

Mini App готов к релизу, когда:

- открывается из Telegram на iOS/Android/Desktop/Web;
- backend валидирует initData и не доверяет client user id;
- Dashboard отображает реальные user-scoped данные;
- все экраны имеют loading / empty / error states;
- History фильтруется без полной загрузки всей истории;
- Analytics не показывает сегментный процент без sample size;
- trend не сравнивает период с отсутствующей выборкой;
- достижения рассчитываются детерминированно;
- UI соблюдает Telegram safe areas и тему;
- мобильные tap targets и контраст проходят базовую accessibility-проверку;
- никаких placeholder probabilities/difficulty не показывается как реальная аналитика;
- Mini App и бот читают одну и ту же БД и не расходятся в результатах прогнозов.

## 19. Product north star

Mini App не должен превращаться в «ещё один экран со статистикой». Его ценность — в объяснимой петле обучения:

**прогноз → результат → паттерн → вывод → следующий прогноз.**
