package telegram

import (
	"bufio"
	"bytes"
	"embed"
	"fmt"
	"strconv"
	"strings"

	"cs2predictor/internal/platform/common"
)

//go:embed i18n/messages_en.properties i18n/messages_ru.properties
var i18nFS embed.FS

// parseProperties reads a minimal subset of the Java .properties format
// (key=value lines, # comments, \n / \\ / \uXXXX escapes) — enough for these
// two files.
func parseProperties(data []byte) (map[string]string, error) {
	out := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "!") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := unescapeProperties(line[idx+1:])
		out[key] = value
	}
	return out, scanner.Err()
}

func unescapeProperties(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i == len(s)-1 {
			b.WriteByte(c)
			continue
		}
		next := s[i+1]
		switch next {
		case 'n':
			b.WriteByte('\n')
			i++
		case 't':
			b.WriteByte('\t')
			i++
		case '\\':
			b.WriteByte('\\')
			i++
		case 'u':
			// Exactly 4 hex digits per the \uXXXX escape, so v is always in
			// [0, 0xFFFF] — always a safe, in-range rune conversion.
			if i+5 < len(s) {
				if v, err := strconv.ParseUint(s[i+2:i+6], 16, 16); err == nil {
					b.WriteRune(rune(v))
					i += 5
					continue
				}
			}
			b.WriteByte(c)
		default:
			b.WriteByte(next)
			i++
		}
	}
	return b.String()
}

// Texts loads the ru/en message bundles and formats them with positional
// {0},{1},... placeholders.
type Texts struct {
	bundles map[common.LocaleCode]map[string]string
}

func LoadTexts() (*Texts, error) {
	en, err := readBundle("i18n/messages_en.properties")
	if err != nil {
		return nil, err
	}
	ru, err := readBundle("i18n/messages_ru.properties")
	if err != nil {
		return nil, err
	}
	return &Texts{bundles: map[common.LocaleCode]map[string]string{
		common.LocaleEN: en,
		common.LocaleRU: ru,
	}}, nil
}

func readBundle(path string) (map[string]string, error) {
	data, err := i18nFS.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseProperties(data)
}

// Get formats bundle[locale][key] with positional {0},{1},... placeholders
// substituted by args (via fmt.Sprint on each). Falls back to RU if locale
// isn't loaded (shouldn't happen — only RU/EN are ever loaded).
func (t *Texts) Get(key string, locale common.LocaleCode, args ...any) string {
	bundle, ok := t.bundles[locale]
	if !ok {
		bundle = t.bundles[common.LocaleRU]
	}
	template, ok := bundle[key]
	if !ok {
		return key
	}
	for i, arg := range args {
		placeholder := fmt.Sprintf("{%d}", i)
		template = strings.ReplaceAll(template, placeholder, fmt.Sprint(arg))
	}
	return template
}
