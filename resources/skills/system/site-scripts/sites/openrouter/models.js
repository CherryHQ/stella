/* @meta
{
  "name": "openrouter/models",
  "description": "搜索 OpenRouter 模型（价格、context length）",
  "domain": "openrouter.ai",
  "args": {
    "query": {"required": true, "description": "搜索关键词，匹配模型 ID 或名称"}
  },
  "readOnly": true,
  "example": "bb-browser site openrouter/models minimax"
}
*/

async function(args) {
  if (!args.query) return {error: 'Missing argument: query'};
  const q = args.query.toLowerCase();

  const resp = await fetch('/api/v1/models');
  if (!resp.ok) return {error: 'Failed to fetch models: HTTP ' + resp.status};
  const data = await resp.json();

  const matches = (data.data || []).filter(m =>
    m.id.toLowerCase().includes(q) || (m.name || '').toLowerCase().includes(q)
  );

  if (matches.length === 0) return {error: 'No models found matching: ' + args.query};

  return matches.map(m => ({
    id: m.id,
    name: m.name,
    context_length: m.context_length,
    pricing: {
      prompt: '$' + (parseFloat(m.pricing?.prompt || 0) * 1e6).toFixed(2) + '/1M',
      completion: '$' + (parseFloat(m.pricing?.completion || 0) * 1e6).toFixed(2) + '/1M'
    },
    modalities: m.architecture?.input_modalities || []
  }));
}
