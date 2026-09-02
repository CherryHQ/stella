/* @meta
{
  "name": "reuters/search",
  "description": "Reuters 路透社新闻搜索",
  "domain": "www.reuters.com",
  "args": {
    "query": {"required": true, "description": "Search query"},
    "count": {"required": false, "description": "Max results to return (default 10)"}
  },
  "readOnly": true,
  "example": "bb-browser site reuters/search \"artificial intelligence\""
}
*/

async function(args) {
  if (!args.query) return {error: 'Missing argument: query', hint: 'Provide a search query string'};
  const count = Math.min(parseInt(args.count) || 10, 40);

  // Strategy 1: Try Reuters Arc API (works when browser has valid session)
  const apiQuery = JSON.stringify({
    keyword: args.query,
    offset: 0,
    orderby: 'display_date:desc',
    size: count,
    website: 'reuters'
  });
  const apiUrl = 'https://www.reuters.com/pf/api/v3/content/fetch/articles-by-search-v2?query=' + encodeURIComponent(apiQuery);

  try {
    const apiResp = await fetch(apiUrl, {credentials: 'include'});
    if (apiResp.ok) {
      const data = await apiResp.json();
      const articles = (data.result?.articles || data.articles || []);
      if (articles.length > 0) {
        const results = articles.slice(0, count).map(a => ({
          title: a.title || a.headlines?.basic || '',
          description: a.description?.basic || a.description || a.subheadlines?.basic || '',
          date: a.display_date || a.published_time || a.first_publish_date || '',
          url: a.canonical_url
            ? 'https://www.reuters.com' + a.canonical_url
            : (a.website_url ? 'https://www.reuters.com' + a.website_url : ''),
          section: a.taxonomy?.section?.name || a.section?.name || '',
          authors: (a.authors || []).map(au => au.name).filter(Boolean).join(', ')
        }));
        return {query: args.query, source: 'api', count: results.length, results};
      }
    }
  } catch (e) {
    // API failed, fall through to HTML parsing
  }

  // Strategy 2: Parse HTML search results page
  const searchUrl = 'https://www.reuters.com/site-search/?query=' + encodeURIComponent(args.query);
  const resp = await fetch(searchUrl, {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status, hint: 'Make sure a reuters.com tab is open so cookies are available'};
  const html = await resp.text();
  const parser = new DOMParser();
  const doc = parser.parseFromString(html, 'text/html');

  const results = [];
  const seen = new Set();

  // Reuters search results are rendered as media-story cards or search result items
  // Try structured selectors first
  const cards = doc.querySelectorAll('[class*="search-result"], [class*="media-story"], [data-testid*="search"], li[class*="story"], article');
  cards.forEach(card => {
    if (results.length >= count) return;
    const anchor = card.querySelector('a[href]');
    if (!anchor) return;
    const href = anchor.getAttribute('href') || '';
    const fullUrl = href.startsWith('http') ? href : 'https://www.reuters.com' + href;
    if (seen.has(fullUrl)) return;
    if (!href.includes('/') || href === '/') return;

    const heading = card.querySelector('h3, h2, h4, [data-testid*="Heading"], span[class*="title"]');
    const title = heading ? heading.textContent.trim() : anchor.textContent.trim();
    if (!title || title.length < 5) return;

    let description = '';
    const pTags = card.querySelectorAll('p, [class*="description"], [class*="snippet"]');
    for (const p of pTags) {
      const txt = p.textContent.trim();
      if (txt.length > 15 && txt !== title) {
        description = txt.substring(0, 500);
        break;
      }
    }

    let date = '';
    const timeEl = card.querySelector('time, [class*="date"], [class*="time"]');
    if (timeEl) {
      date = timeEl.getAttribute('datetime') || timeEl.textContent.trim();
    }

    seen.add(fullUrl);
    results.push({title, description, date, url: fullUrl});
  });

  // Fallback: broad link scan if structured selectors found nothing
  if (results.length === 0) {
    const links = doc.querySelectorAll('a[href]');
    links.forEach(a => {
      if (results.length >= count) return;
      const href = a.getAttribute('href') || '';
      // Keep only article-like paths
      if (!(href.includes('/world/') || href.includes('/business/') || href.includes('/markets/') ||
            href.includes('/technology/') || href.includes('/science/') || href.includes('/sports/') ||
            href.includes('/legal/') || href.includes('/sustainability/'))) return;
      const fullUrl = href.startsWith('http') ? href : 'https://www.reuters.com' + href;
      if (seen.has(fullUrl)) return;

      const heading = a.querySelector('h1, h2, h3, h4, span');
      const title = heading ? heading.textContent.trim() : a.textContent.trim();
      if (!title || title.length < 8) return;

      seen.add(fullUrl);

      let description = '';
      const parent = a.closest('li, div, article, section');
      if (parent) {
        const pTags = parent.querySelectorAll('p');
        for (const p of pTags) {
          const txt = p.textContent.trim();
          if (txt.length > 15 && txt !== title) {
            description = txt.substring(0, 500);
            break;
          }
        }
      }

      results.push({title, description, url: fullUrl});
    });
  }

  if (results.length === 0) {
    return {query: args.query, count: 0, results: [], hint: 'No results found. Ensure reuters.com is open in a browser tab and you are not blocked by captcha.'};
  }

  return {query: args.query, source: 'html', count: results.length, results};
}
