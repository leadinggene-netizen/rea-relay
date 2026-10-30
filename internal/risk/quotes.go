package risk

// MapQuoteSource is a trivial in-memory QuoteSource keyed by broker symbol,
// used by the HTTP entrypoint and tests.
type MapQuoteSource map[string]Quote

// Quote implements QuoteSource.
func (m MapQuoteSource) Quote(brokerSymbol string) (Quote, bool) {
	q, ok := m[brokerSymbol]
	return q, ok
}
