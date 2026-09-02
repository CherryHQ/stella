/* @meta
{
  "name": "youtube/feed",
  "description": "Get YouTube home feed or subscriptions feed",
  "domain": "www.youtube.com",
  "args": {
    "type": {"required": false, "description": "Feed type: 'home' (default) or 'subscriptions'"},
    "max": {"required": false, "description": "Max videos to return (default: 20)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site youtube/feed subscriptions"
}
*/

async function(args) {
  const feedType = (args.type || 'home').toLowerCase();
  const max = Math.min(parseInt(args.max) || 20, 50);

  if (feedType !== 'home' && feedType !== 'subscriptions') {
    return {error: 'Invalid feed type', hint: 'Use "home" or "subscriptions"'};
  }

  const cfg = window.ytcfg?.data_ || {};
  const apiKey = cfg.INNERTUBE_API_KEY;
  const context = cfg.INNERTUBE_CONTEXT;
  if (!apiKey || !context) return {error: 'YouTube config not found', hint: 'Make sure you are on youtube.com'};

  // Helper: build SAPISIDHASH for authenticated requests
  async function buildAuth() {
    const cookies = document.cookie.split(';').map(c => c.trim());
    const sapisidCookie = cookies.find(c => c.startsWith('__Secure-3PAPISID='));
    if (!sapisidCookie) return null;
    const sapisidValue = sapisidCookie.split('=')[1];
    const timestamp = Math.floor(Date.now() / 1000);
    const origin = 'https://www.youtube.com';
    const encoder = new TextEncoder();
    const data = encoder.encode(timestamp + ' ' + sapisidValue + ' ' + origin);
    const hashBuffer = await crypto.subtle.digest('SHA-1', data);
    const hashArray = Array.from(new Uint8Array(hashBuffer));
    const hashHex = hashArray.map(b => b.toString(16).padStart(2, '0')).join('');
    return 'SAPISIDHASH ' + timestamp + '_' + hashHex;
  }

  // Helper: extract videos from lockupViewModel format
  function extractFromLockup(lvm) {
    if (!lvm || lvm.contentType !== 'LOCKUP_CONTENT_TYPE_VIDEO') return null;
    const meta = lvm.metadata?.lockupMetadataViewModel;
    const rows = meta?.metadata?.contentMetadataViewModel?.metadataRows || [];
    let channel = '', viewsAndTime = '';
    if (rows[0]?.metadataParts?.[0]) channel = rows[0].metadataParts[0].text?.content || '';
    if (rows[1]?.metadataParts) viewsAndTime = rows[1].metadataParts.map(p => p.text?.content).filter(Boolean).join(' | ');
    // If only 1 row of metadata, views/time is in first row's second part
    if (!viewsAndTime && rows[0]?.metadataParts?.length > 1) {
      viewsAndTime = rows[0].metadataParts.slice(1).map(p => p.text?.content).filter(Boolean).join(' | ');
    }
    let duration = '';
    const overlays = lvm.contentImage?.thumbnailViewModel?.overlays || [];
    for (const ov of overlays) {
      for (const b of (ov.thumbnailBottomOverlayViewModel?.badges || [])) {
        if (b.thumbnailBadgeViewModel?.text) duration = b.thumbnailBadgeViewModel.text;
      }
    }
    return {
      videoId: lvm.contentId,
      title: meta?.title?.content || '',
      channel,
      duration,
      viewsAndTime,
      url: 'https://www.youtube.com/watch?v=' + lvm.contentId
    };
  }

  // For home feed, try ytInitialData first if on home page
  if (feedType === 'home' && location.pathname === '/') {
    const d = window.ytInitialData;
    if (d) {
      const tabs = d.contents?.twoColumnBrowseResultsRenderer?.tabs || [];
      const richGrid = tabs[0]?.tabRenderer?.content?.richGridRenderer;
      if (richGrid) {
        const videos = [];
        for (const item of (richGrid.contents || [])) {
          if (videos.length >= max) break;
          const lvm = item.richItemRenderer?.content?.lockupViewModel;
          const video = extractFromLockup(lvm);
          if (video) videos.push(video);
        }
        return {feed: 'home', source: 'page', videoCount: videos.length, videos};
      }
    }
  }

  // Use innertube browse API
  const browseId = feedType === 'subscriptions' ? 'FEsubscriptions' : 'FEwhat_to_watch';
  const authHeader = await buildAuth();
  const headers = {'Content-Type': 'application/json'};
  if (authHeader) {
    headers['Authorization'] = authHeader;
    headers['X-Goog-AuthUser'] = '0';
    headers['X-Origin'] = 'https://www.youtube.com';
  }

  const resp = await fetch('/youtubei/v1/browse?key=' + apiKey + '&prettyPrint=false', {
    method: 'POST',
    credentials: 'include',
    headers,
    body: JSON.stringify({context, browseId})
  });

  if (!resp.ok) return {error: 'Feed API returned HTTP ' + resp.status};
  const data = await resp.json();

  const tabs = data.contents?.twoColumnBrowseResultsRenderer?.tabs || [];
  const videos = [];

  for (const tab of tabs) {
    // richGridRenderer (home/subs)
    const richGrid = tab.tabRenderer?.content?.richGridRenderer;
    if (richGrid) {
      for (const item of (richGrid.contents || [])) {
        if (videos.length >= max) break;
        const lvm = item.richItemRenderer?.content?.lockupViewModel;
        const video = extractFromLockup(lvm);
        if (video) videos.push(video);
      }
    }

    // sectionListRenderer (fallback)
    const sectionList = tab.tabRenderer?.content?.sectionListRenderer;
    if (sectionList && videos.length === 0) {
      for (const section of (sectionList.contents || [])) {
        const items = section.itemSectionRenderer?.contents || [];
        for (const item of items) {
          if (item.backgroundPromoRenderer) {
            return {
              error: 'Not logged in for subscriptions',
              hint: item.backgroundPromoRenderer.bodyText?.runs?.[0]?.text || 'Sign in to see subscriptions'
            };
          }
        }
      }
    }
  }

  if (videos.length === 0 && feedType === 'subscriptions') {
    return {error: 'No subscription videos found', hint: 'You may not be logged in or have no subscriptions'};
  }

  return {
    feed: feedType,
    source: 'api',
    videoCount: videos.length,
    videos
  };
}
