package nodecore

import (
	"encoding/json"
	"strings"

	"github.com/mathmach/gpu-grid-worker/pkg/domain/derr"
)

type DisplayCurrency string

const (
	CurrencyUSD DisplayCurrency = "USD"
	CurrencyBRL DisplayCurrency = "BRL"
)

func ParseDisplayCurrency(raw string) (DisplayCurrency, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case string(CurrencyUSD):
		return CurrencyUSD, nil
	case string(CurrencyBRL):
		return CurrencyBRL, nil
	default:
		return "", derr.Validationf(derr.CodeValidation, "unsupported display currency %q (expected USD or BRL)", raw)
	}
}

func (c DisplayCurrency) String() string               { return string(c) }
func (c DisplayCurrency) IsZero() bool                 { return strings.TrimSpace(string(c)) == "" }
func (c DisplayCurrency) MarshalJSON() ([]byte, error) { return json.Marshal(string(c)) }

func (c *DisplayCurrency) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return derr.Wrap(derr.CodeValidation, 400, "invalid currency JSON string", err)
	}
	parsed, err := ParseDisplayCurrency(raw)
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}
