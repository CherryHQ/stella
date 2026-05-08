const translations = {
  en: {
    // Nav
    about: "About",
    docs: "Docs",
    // Index - Hero
    heroTag: "AI assistant / self-hosted",
    heroTitle1: "Your assistant",
    heroTitle2: "that never",
    heroTitle3: "forgets",
    heroDescription:
      "Single binary, lossless context management. Talk from your terminal or any messenger — stella remembers everything.",
    readTheDocs: "Read the docs",
    sourceOnGithub: "Source on GitHub",
    // Index - Features
    featuresTitle: "What makes stella different",
    feature1Title: "Lossless memory",
    feature1Body:
      "DAG-based context compression. Conversations grow without bounds and without losing a single detail. Every thread, every tangent, preserved.",
    feature2Title: "Multi-channel",
    feature2Body:
      "Terminal TUI, Telegram, QQ, Feishu. All channels share the same session and memory. Start a thought in your terminal, pick it up on Telegram.",
    feature3Title: "Self-hosted",
    feature3Body:
      "Single Go binary + SQLite. Your machine, your API keys. Nothing leaves your network. Deploy with Docker, systemd, or just run the binary.",
    feature4Title: "Built-in scheduler",
    feature4Body:
      "Scheduled tasks, heartbeat monitoring, and cross-channel notifications. stella works even when you're not talking to it.",
    // Index - Meet Stella
    meetStella: "Meet Stella",
    meetStellaTitle: "A calm digital companion",
    meetStellaBody1:
      "Stella is more than a tool — she is a quiet, trustworthy assistant designed for the long run. She remembers your context, connects your workflows across devices, and stays reliably present without getting in the way.",
    meetStellaBody2:
      "Built with real warmth and digital precision. Local-first, memory-aware, and always composed.",
    learnMoreAboutStella: "Learn more about Stella",
    // Index - Footer CTA
    getStarted: "Get started in seconds",
    getStartedBody: "One binary, one config file. No containers required.",
    // About - Hero
    aboutHeroTitle1: "A quiet,",
    aboutHeroTitle2: "reliable",
    aboutHeroTitle3: "companion",
    aboutHeroDescription:
      "Stella is a self-hosted AI assistant with real warmth and digital precision. She remembers what you said, connects your workflows across devices, and stays quietly useful — day after day.",
    // About - Traits
    traitsTitle: "What defines Stella",
    traitsIntro: "Stella is not another generic AI chatbot. She is a",
    traitsIntroBold: "calm digital companion",
    traitsIntroEnd: "— designed to be trustworthy, long-lasting, and deeply aware of your context.",
    traitCalm: "Calm",
    traitCalmDesc: "Quiet intelligence over loud marketing. Stella speaks with clarity, not hype.",
    traitTrustworthy: "Trustworthy",
    traitTrustworthyDesc:
      "Reliable and consistent. She remembers your context and never loses a detail.",
    traitMemoryAware: "Memory-aware",
    traitMemoryAwareDesc:
      "Long-term context is a first-class feature, not an afterthought. Every conversation builds on the last.",
    traitLocalFirst: "Local-first",
    traitLocalFirstDesc:
      "Your machine, your data. Stella runs as a single binary with SQLite — nothing leaves your network.",
    traitCompanion: "Companion",
    traitCompanionDesc:
      "Not a one-shot tool. Stella is designed for the long run — a digital assistant that grows with you.",
    traitElegant: "Elegant",
    traitElegantDesc:
      "Minimal and restrained. No clutter, no noise. Just the right amount of presence.",
    // About - Visual Identity
    visualIdentity: "Visual identity",
    visualIdentityIntro:
      "Stella's look is intentional: a semi-realistic portrait that feels human but is clearly a brand character. She looks like someone real — but she is a digital assistant, and that distinction matters.",
    idPalette: "Palette",
    idPaletteDetail:
      "Deep navy blue with champagne gold accents. Blue carries trust and calm; gold carries warmth and memory.",
    idExpression: "Expression",
    idExpressionDetail:
      "Composed, gentle, attentive. Never overly cheerful, never cold. The feeling of someone who listens.",
    idBackground: "Background",
    idBackgroundDetail:
      "Subtle memory-network motifs — faint nodes and connection lines that hint at long-term context without overwhelming.",
    idStyle: "Style",
    idStyleDetail:
      "70% photographic realism, 30% brand stylization. Natural skin texture, soft portrait lighting, recognizable at small sizes.",
    swatchNavy: "Navy",
    swatchMidBlue: "Mid Blue",
    swatchGold: "Gold",
    swatchCaption: "Deep navy + champagne gold",
    // About - Closing
    builtToStay: "Built to stay",
    builtToStayBody:
      "Stella is not about making noise. She earns trust over time — through consistent memory, reliable assistance, and quiet presence. The kind of assistant you keep coming back to.",
  },
  zh: {
    about: "关于",
    docs: "文档",
    heroTag: "AI 助手 / 自托管",
    heroTitle1: "你的助手",
    heroTitle2: "永远不会",
    heroTitle3: "遗忘",
    heroDescription: "单一二进制文件，无损上下文管理。在终端或任何即时通讯中对话——stella 记住一切。",
    readTheDocs: "阅读文档",
    sourceOnGithub: "GitHub 源码",
    featuresTitle: "stella 的独特之处",
    feature1Title: "无损记忆",
    feature1Body:
      "基于 DAG 的上下文压缩。对话无限增长而不丢失任何细节。每个线程、每个分支，完整保留。",
    feature2Title: "多渠道",
    feature2Body:
      "终端 TUI、Telegram、QQ、飞书。所有渠道共享同一会话和记忆。在终端开始的想法，可以在 Telegram 上继续。",
    feature3Title: "自托管",
    feature3Body:
      "单一 Go 二进制文件 + SQLite。你的机器，你的 API 密钥。数据不会离开你的网络。支持 Docker、systemd 或直接运行。",
    feature4Title: "内置调度器",
    feature4Body: "定时任务、心跳监控和跨渠道通知。即使你不和 stella 对话，她也在工作。",
    meetStella: "认识 Stella",
    meetStellaTitle: "一位沉静的数字伙伴",
    meetStellaBody1:
      "Stella 不仅仅是一个工具——她是一位安静、值得信赖的助手，为长期陪伴而设计。她记住你的上下文，连接你跨设备的工作流，始终可靠地陪伴而不打扰。",
    meetStellaBody2: "融合真实温度与数字精确。本地优先、记忆感知、始终沉着。",
    learnMoreAboutStella: "了解更多关于 Stella",
    getStarted: "几秒钟即可开始",
    getStartedBody: "一个二进制文件，一个配置文件。无需容器。",
    aboutHeroTitle1: "安静、",
    aboutHeroTitle2: "可靠的",
    aboutHeroTitle3: "伙伴",
    aboutHeroDescription:
      "Stella 是一个自托管的 AI 助手，兼具真实温度与数字精确。她记住你说过的话，连接你跨设备的工作流，日复一日地安静而有用。",
    traitsTitle: "Stella 的特质",
    traitsIntro: "Stella 不是另一个普通的 AI 聊天机器人。她是一位",
    traitsIntroBold: "沉静的数字伙伴",
    traitsIntroEnd: "——值得信赖、持久陪伴、深刻感知你的上下文。",
    traitCalm: "沉静",
    traitCalmDesc: "安静的智慧胜过喧嚣的营销。Stella 以清晰而非炒作来表达。",
    traitTrustworthy: "可信赖",
    traitTrustworthyDesc: "可靠而一致。她记住你的上下文，不遗漏任何细节。",
    traitMemoryAware: "记忆感知",
    traitMemoryAwareDesc: "长期上下文是核心功能，而非事后补充。每次对话都建立在上一次的基础之上。",
    traitLocalFirst: "本地优先",
    traitLocalFirstDesc:
      "你的机器，你的数据。Stella 以单一二进制文件和 SQLite 运行——数据不会离开你的网络。",
    traitCompanion: "陪伴",
    traitCompanionDesc: "不是一次性工具。Stella 为长期陪伴而设计——一位与你共同成长的数字助手。",
    traitElegant: "优雅",
    traitElegantDesc: "简约而克制。没有杂乱，没有噪音。恰到好处的存在感。",
    visualIdentity: "视觉形象",
    visualIdentityIntro:
      "Stella 的外观是精心设计的：一幅半写实的肖像，既有人的感觉又明显是品牌角色。她看起来像一个真实的人——但她是数字助手，这种区分很重要。",
    idPalette: "配色",
    idPaletteDetail: "深海军蓝搭配香槟金色调。蓝色承载信任与沉静；金色承载温暖与记忆。",
    idExpression: "表情",
    idExpressionDetail: "沉着、温和、专注。从不过分开朗，也不冷漠。一种倾听者的感觉。",
    idBackground: "背景",
    idBackgroundDetail: "微妙的记忆网络图案——淡淡的节点和连接线暗示长期上下文，而不会喧宾夺主。",
    idStyle: "风格",
    idStyleDetail: "70% 摄影写实，30% 品牌风格化。自然肤质、柔和人像光、在小尺寸下依然可辨识。",
    swatchNavy: "海军蓝",
    swatchMidBlue: "中蓝",
    swatchGold: "金色",
    swatchCaption: "深海军蓝 + 香槟金",
    builtToStay: "为留下而生",
    builtToStayBody:
      "Stella 不追求喧嚣。她通过持久的记忆、可靠的帮助和安静的陪伴赢得信任。那种你会一直回来找的助手。",
  },
} as const;

type Locale = keyof typeof translations;

export function t(lang: string) {
  const locale = (lang in translations ? lang : "en") as Locale;
  return translations[locale];
}
