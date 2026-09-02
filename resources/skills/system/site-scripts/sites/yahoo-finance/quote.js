/* @meta
{
  "name": "yahoo-finance/quote",
  "description": "Yahoo Finance 股票行情",
  "domain": "finance.yahoo.com",
  "args": {
    "symbol": {"required": true, "description": "Stock ticker symbol, e.g. AAPL, MSFT, TSLA"}
  },
  "readOnly": true,
  "example": "bb-browser site yahoo-finance/quote AAPL"
}
*/

async function(args) {
  if (!args.symbol) return {error: 'Missing argument: symbol', hint: 'Please provide a stock ticker symbol, e.g. AAPL'};

  var symbol = args.symbol.toUpperCase().trim();

  // Try v7 quote API first
  var url = 'https://query1.finance.yahoo.com/v7/finance/quote?symbols=' + encodeURIComponent(symbol);
  var resp;
  var data;

  try {
    resp = await fetch(url, {
      headers: {
        'User-Agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36',
        'Accept': 'application/json'
      }
    });

    if (resp.ok) {
      data = await resp.json();
      var results = data && data.quoteResponse && data.quoteResponse.result;
      if (results && results.length > 0) {
        var q = results[0];
        return formatQuote(q);
      }
    }
  } catch(e) {
    // v7 failed (likely CORS), fall through to alternatives
  }

  // Fallback: try v8 chart API
  try {
    var chartUrl = 'https://query1.finance.yahoo.com/v8/finance/chart/' + encodeURIComponent(symbol) + '?interval=1d&range=1d';
    resp = await fetch(chartUrl, {
      headers: {
        'User-Agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36',
        'Accept': 'application/json'
      }
    });

    if (resp.ok) {
      data = await resp.json();
      var chart = data && data.chart && data.chart.result && data.chart.result[0];
      if (chart) {
        return formatChart(chart, symbol);
      }
    }
  } catch(e) {
    // v8 also failed, fall through
  }

  // Fallback: scrape from Yahoo Finance page via page context
  try {
    var pageUrl = 'https://finance.yahoo.com/quote/' + encodeURIComponent(symbol) + '/';
    resp = await fetch(pageUrl, {
      headers: {
        'User-Agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36',
        'Accept': 'text/html'
      }
    });

    if (resp.ok) {
      var html = await resp.text();

      // Try to extract JSON data embedded in the page
      var jsonMatch = html.match(/root\.App\.main\s*=\s*(\{.*?\});\s*\n/);
      if (jsonMatch) {
        var appData = JSON.parse(jsonMatch[1]);
        var stores = appData && appData.context && appData.context.dispatcher && appData.context.dispatcher.stores;
        if (stores && stores.QuoteSummaryStore && stores.QuoteSummaryStore.price) {
          var price = stores.QuoteSummaryStore.price;
          return {
            symbol: symbol,
            name: price.shortName || price.longName || symbol,
            price: price.regularMarketPrice && price.regularMarketPrice.raw,
            change: price.regularMarketChange && price.regularMarketChange.raw,
            changePercent: price.regularMarketChangePercent && price.regularMarketChangePercent.raw
              ? (price.regularMarketChangePercent.raw * 100).toFixed(2) + '%' : null,
            currency: price.currency,
            exchange: price.exchangeName,
            marketState: price.marketState,
            url: pageUrl
          };
        }
      }

      // Try to extract from newer page format (FinanceConfigStore or similar)
      var titleMatch = html.match(/<title>([^<]+)<\/title>/);
      var priceMatch = html.match(/data-testid="qsp-price"[^>]*>([^<]+)</);
      var changeMatch = html.match(/data-testid="qsp-price-change"[^>]*>([^<]+)</);
      var changePctMatch = html.match(/data-testid="qsp-price-change-percent"[^>]*>([^<]+)</);

      if (priceMatch) {
        return {
          symbol: symbol,
          name: titleMatch ? titleMatch[1].split('(')[0].trim() : symbol,
          price: priceMatch[1].replace(/,/g, ''),
          change: changeMatch ? changeMatch[1] : null,
          changePercent: changePctMatch ? changePctMatch[1] : null,
          source: 'page-scrape',
          url: pageUrl
        };
      }

      return {error: 'Could not parse quote data from page', symbol: symbol, url: pageUrl};
    }
  } catch(e) {
    // All methods failed
  }

  return {error: 'Failed to fetch quote for ' + symbol, hint: 'All API methods failed. The symbol may be invalid or Yahoo Finance may be blocking requests.'};

  function formatQuote(q) {
    return {
      symbol: q.symbol,
      name: q.shortName || q.longName || q.symbol,
      price: q.regularMarketPrice,
      change: q.regularMarketChange != null ? q.regularMarketChange.toFixed(2) : null,
      changePercent: q.regularMarketChangePercent != null ? q.regularMarketChangePercent.toFixed(2) + '%' : null,
      open: q.regularMarketOpen,
      high: q.regularMarketDayHigh,
      low: q.regularMarketDayLow,
      prevClose: q.regularMarketPreviousClose,
      volume: q.regularMarketVolume,
      marketCap: formatLargeNumber(q.marketCap),
      pe: q.trailingPE != null ? q.trailingPE.toFixed(2) : null,
      eps: q.epsTrailingTwelveMonths != null ? q.epsTrailingTwelveMonths.toFixed(2) : null,
      week52High: q.fiftyTwoWeekHigh,
      week52Low: q.fiftyTwoWeekLow,
      avgVolume: q.averageDailyVolume3Month,
      currency: q.currency,
      exchange: q.fullExchangeName || q.exchange,
      marketState: q.marketState,
      quoteType: q.quoteType,
      url: 'https://finance.yahoo.com/quote/' + q.symbol + '/'
    };
  }

  function formatChart(chart, sym) {
    var meta = chart.meta || {};
    var indicators = chart.indicators;
    var quoteData = indicators && indicators.quote && indicators.quote[0];
    var timestamps = chart.timestamp || [];

    var lastIdx = timestamps.length - 1;
    var currentPrice = meta.regularMarketPrice || (quoteData && quoteData.close && quoteData.close[lastIdx]);
    var prevClose = meta.previousClose || meta.chartPreviousClose;
    var change = (currentPrice != null && prevClose != null) ? (currentPrice - prevClose) : null;
    var changePct = (change != null && prevClose) ? ((change / prevClose) * 100) : null;

    return {
      symbol: meta.symbol || sym,
      name: meta.shortName || meta.longName || sym,
      price: currentPrice != null ? Number(currentPrice.toFixed(2)) : null,
      change: change != null ? change.toFixed(2) : null,
      changePercent: changePct != null ? changePct.toFixed(2) + '%' : null,
      open: quoteData && quoteData.open ? quoteData.open[0] : null,
      high: quoteData && quoteData.high ? Math.max.apply(null, quoteData.high.filter(function(v) { return v != null; })) : null,
      low: quoteData && quoteData.low ? Math.min.apply(null, quoteData.low.filter(function(v) { return v != null; })) : null,
      prevClose: prevClose,
      volume: meta.regularMarketVolume || (quoteData && quoteData.volume ? quoteData.volume[lastIdx] : null),
      currency: meta.currency,
      exchange: meta.exchangeName,
      marketState: meta.marketState,
      source: 'chart-api',
      url: 'https://finance.yahoo.com/quote/' + (meta.symbol || sym) + '/'
    };
  }

  function formatLargeNumber(v) {
    if (v == null) return null;
    if (v >= 1e12) return (v / 1e12).toFixed(2) + 'T';
    if (v >= 1e9) return (v / 1e9).toFixed(2) + 'B';
    if (v >= 1e6) return (v / 1e6).toFixed(2) + 'M';
    if (v >= 1e3) return (v / 1e3).toFixed(2) + 'K';
    return v.toString();
  }
}
