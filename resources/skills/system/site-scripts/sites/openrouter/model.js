/* @meta
{
  "name": "openrouter/model",
  "description": "查询 OpenRouter 模型详情（价格 + 各 provider 性能数据）",
  "domain": "openrouter.ai",
  "args": {
    "id": {"required": true, "description": "模型 ID（如 minimax/minimax-m2.7）"}
  },
  "readOnly": true,
  "example": "bb-browser site openrouter/model minimax/minimax-m2.7"
}
*/

async function(args) {
  if (!args.id) return {error: 'Missing argument: id'};

  const modelsResp = await fetch('/api/v1/models');
  if (!modelsResp.ok) return {error: 'Failed to fetch models: HTTP ' + modelsResp.status};
  const modelsData = await modelsResp.json();

  const model = (modelsData.data || []).find(m => m.id === args.id);
  if (!model) return {error: 'Model not found: ' + args.id, hint: 'Try: bb-browser site openrouter/models <keyword>'};

  const slug = encodeURIComponent(model.canonical_slug || model.id);
  const base = '/api/frontend/stats';

  const [endpointResp, tpResp, latResp, e2eResp] = await Promise.all([
    fetch(base + '/endpoint?permaslug=' + slug + '&variant=standard').then(r => r.ok ? r.json() : null),
    fetch(base + '/throughput-comparison?permaslug=' + slug).then(r => r.ok ? r.json() : null),
    fetch(base + '/latency-comparison?permaslug=' + slug).then(r => r.ok ? r.json() : null),
    fetch(base + '/latency-e2e-comparison?permaslug=' + slug).then(r => r.ok ? r.json() : null),
  ]);

  const providerMap = {};
  if (endpointResp?.data) {
    for (const ep of endpointResp.data) {
      const name = (ep.name || '').split('|')[0].trim();
      providerMap[ep.id] = name || ep.id;
    }
  }

  function getLatest(statsData) {
    if (!statsData?.data?.length) return {};
    const latest = statsData.data[statsData.data.length - 1];
    return latest?.y || {};
  }

  const tp = getLatest(tpResp);
  const lat = getLatest(latResp);
  const e2e = getLatest(e2eResp);

  const allIds = new Set([...Object.keys(tp), ...Object.keys(lat), ...Object.keys(e2e)]);
  const performance = [];

  for (const id of allIds) {
    performance.push({
      provider: providerMap[id] || id,
      throughput_tps: tp[id] ?? null,
      latency_s: lat[id] ? +(lat[id] / 1000).toFixed(2) : null,
      e2e_latency_s: e2e[id] ? +(e2e[id] / 1000).toFixed(2) : null,
    });
  }

  performance.sort((a, b) => (b.throughput_tps || 0) - (a.throughput_tps || 0));

  return {
    id: model.id,
    name: model.name,
    context_length: model.context_length,
    pricing: {
      prompt: '$' + (parseFloat(model.pricing?.prompt || 0) * 1e6).toFixed(2) + '/1M',
      completion: '$' + (parseFloat(model.pricing?.completion || 0) * 1e6).toFixed(2) + '/1M'
    },
    weekly_tokens: model.top_provider?.weekly_tokens || null,
    performance
  };
}
