/* @meta
{
  "name": "youtube/search",
  "description": "Search YouTube videos",
  "domain": "www.youtube.com",
  "args": {
    "query": {"required": true, "description": "Search query string"},
    "max": {"required": false, "description": "Max results to return (default: 20)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site youtube/search 'TypeScript tutorial'"
}
*/

async function(args) {
  if (!args.query) return {error: 'Missing argument: query', hint: 'Provide a search query'};

  const cfg = window.ytcfg?.data_ || {};
  const apiKey = cfg.INNERTUBE_API_KEY;
  const context = cfg.INNERTUBE_CONTEXT;
  if (!apiKey || !context) return {error: 'YouTube config not found', hint: 'Make sure you are on youtube.com'};

  const max = Math.min(parseInt(args.max) || 20, 50);

  const resp = await fetch('/youtubei/v1/search?key=' + apiKey + '&prettyPrint=false', {
    method: 'POST',
    credentials: 'include',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({context, query: args.query})
  });

  if (!resp.ok) return {error: 'Search API returned HTTP ' + resp.status, hint: 'YouTube API error'};

  const data = await resp.json();
  const contents = data.contents?.twoColumnSearchResultsRenderer?.primaryContents?.sectionListRenderer?.contents || [];

  const videos = [];
  for (const section of contents) {
    const items = section.itemSectionRenderer?.contents || [];
    for (const item of items) {
      if (item.videoRenderer && videos.length < max) {
        const v = item.videoRenderer;
        videos.push({
          videoId: v.videoId,
          title: v.title?.runs?.[0]?.text,
          channel: v.ownerText?.runs?.[0]?.text,
          channelId: v.ownerText?.runs?.[0]?.navigationEndpoint?.browseEndpoint?.browseId,
          views: v.viewCountText?.simpleText || v.shortViewCountText?.simpleText || '',
          duration: v.lengthText?.simpleText || 'LIVE',
          publishedTime: v.publishedTimeText?.simpleText || '',
          description: (v.detailedMetadataSnippets?.[0]?.snippetText?.runs?.map(r => r.text).join('') || '').substring(0, 200),
          url: 'https://www.youtube.com/watch?v=' + v.videoId
        });
      }
    }
  }

  return {
    query: args.query,
    resultCount: videos.length,
    videos
  };
}
