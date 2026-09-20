# Layout QA — Multi-Game Analyst 2.1

## Проверено

Визуальный render review выполнен для всех пяти основных экранов на базовом mobile frame 390×844: Dashboard, Analytics, History, Achievements, Profile. Дополнительно Dashboard проверен на compact width 320 px и длинным full-page render для контроля поведения карточек при скролле.

## Результат

- обычный контент не использует фиксированные высоты для текста;
- сетки с текстом используют `minmax(0,1fr)` / `min-width:0`;
- карточки растут по контенту;
- team/event labels в плотных строках имеют controlled ellipsis;
- fixed bottom navigation компенсируется нижним padding основного контента;
- единственные намеренные horizontal scroll areas — GameRail и filter rail;
- на 320 px page padding и gaps уменьшаются отдельным breakpoint;
- декоративные absolute layers находятся внутри `overflow:hidden` карточек и не участвуют в layout;
- logo component имеет initials fallback при недоступности внешнего image URL;
- navigation и result states не зависят только от цвета.

## Файлы для визуальной проверки

- `figma/predictor-multigame-board.png`
- `figma/v2/viewport-final/*.png`
- `figma/v2/screens/*.png`

## Перед production

Повторить screenshot regression на целевых Telegram WebView: iOS small/large, Android 360/390/412 и Desktop narrow. Отдельно проверить реальные длинные локализованные названия турниров/команд и системный font scaling.
