/* @meta
{
  "name": "youdao/translate",
  "description": "有道翻译/词典查询",
  "domain": "dict.youdao.com",
  "args": {
    "query": {"required": true, "description": "Word or sentence to translate (English or Chinese)"}
  },
  "capabilities": ["network"],
  "readOnly": true,
  "example": "bb-browser site youdao/translate hello"
}
*/

async function(args) {
  if (!args.query) return {error: 'Missing argument: query'};
  var q = args.query.trim();

  var resp = await fetch('https://dict.youdao.com/jsonapi_s?doctype=json&jsonversion=4&q=' + encodeURIComponent(q), {credentials: 'include'});
  if (!resp.ok) return {error: 'HTTP ' + resp.status};
  var d = await resp.json();

  var result = {query: q, language: (d.meta && d.meta.guessLanguage) || null};

  // --- Sentence translation (fanyi) ---
  if (d.fanyi && d.fanyi.tran) {
    result.translation = {
      type: d.fanyi.type || null,
      result: d.fanyi.tran
    };
  }

  // --- Phonetic (simple) ---
  if (d.simple && d.simple.word && d.simple.word[0]) {
    var w = d.simple.word[0];
    result.phonetic = {};
    if (w.usphone) result.phonetic.us = w.usphone;
    if (w.ukphone) result.phonetic.uk = w.ukphone;
    if (w.phone) result.phonetic.pinyin = w.phone;
  }

  // --- English-Chinese dict (ec) ---
  if (d.ec && d.ec.word && d.ec.word.trs) {
    result.definitions = d.ec.word.trs.map(function(t) {
      return {pos: t.pos || '', meaning: t.tran || ''};
    });
    // Exam types
    if (d.ec.exam_type) result.examTypes = d.ec.exam_type;
    // Word forms
    if (d.ec.word.wfs) {
      result.wordForms = d.ec.word.wfs.map(function(f) {
        return {type: f.wf.name, value: f.wf.value};
      });
    }
  }

  // --- Chinese-English dict (ce) ---
  if (d.ce && d.ce.word && d.ce.word.trs) {
    result.definitions = d.ce.word.trs.map(function(t) {
      return {word: t['#text'] || '', meaning: t['#tran'] || ''};
    });
  }

  // --- Web translations ---
  if (d.web_trans && d.web_trans['web-translation']) {
    var wt = d.web_trans['web-translation'];
    result.webTranslations = wt.slice(0, 5).map(function(item) {
      return {
        key: item.key,
        values: (item.trans || []).map(function(t) { return t.value; }).filter(Boolean)
      };
    });
  }

  // --- Phrases ---
  if (d.phrs && d.phrs.phrs) {
    result.phrases = d.phrs.phrs.slice(0, 6).map(function(p) {
      return {phrase: p.headword, meaning: p.translation};
    });
  }

  // --- Bilingual example sentences ---
  if (d.blng_sents_part && d.blng_sents_part['sentence-pair']) {
    result.examples = d.blng_sents_part['sentence-pair'].slice(0, 5).map(function(s) {
      return {
        en: (s.sentence || '').replace(/<[^>]+>/g, ''),
        zh: (s['sentence-translation'] || '').replace(/<[^>]+>/g, '')
      };
    });
  }

  // --- Synonyms ---
  if (d.syno && d.syno.synos) {
    result.synonyms = d.syno.synos.slice(0, 4).map(function(s) {
      return {pos: s.pos || '', words: (s.ws || []).map(function(w) { return w.w; })};
    });
  }

  // --- Etymology ---
  if (d.etym && d.etym.etyms && d.etym.etyms.zh) {
    result.etymology = d.etym.etyms.zh.map(function(e) { return e.value || e; }).join(' ');
  }

  return result;
}
