const translations = {
  en: {
    // Nav
    about: "About",
    docs: "Docs",
    // Index - Hero
    heroTag: "AI assistant / self-hosted",
    heroTitle1: "Your agent",
    heroTitle2: "that just",
    heroTitle3: "works",
    heroDescription:
      "Stella remembers every conversation, connects to your messengers, and handles tasks on your behalf. One binary, your machine, your data.",
    readTheDocs: "Read the docs",
    sourceOnGithub: "Source on GitHub",
    // Index - Conversation
    memoryLabel: "Memory that lasts",
    memoryTitle: "She remembers so you don't have to",
    memoryBody:
      "Ask about last week's decisions or a detail from months ago. Stella keeps the full picture and recalls it when you need it.",
    conversationLabel: "conversation",
    // Index - Features
    featuresTitle: "What makes Stella different",
    feature1Title: "Remembers everything",
    feature1Body:
      "Your conversations grow without bounds. Stella compresses older context in the background but never loses a detail. Ask about anything you discussed, anytime.",
    feature2Title: "Works across channels",
    feature2Body:
      "Telegram, QQ, Feishu, WeChat, or the Web UI. All channels share the same memory. Start on your phone, continue on your laptop.",
    feature3Title: "Runs on your machine",
    feature3Body:
      "Single binary, SQLite, your API keys. Nothing leaves your network. Install with Homebrew, Docker, or just download the binary.",
    feature4Title: "Works while you're away",
    feature4Body:
      "Schedule reminders, recurring checks, RSS digests, and automated tasks. Stella keeps running and notifies you when something needs attention.",
    feature5Title: "Async agent work",
    feature5Body:
      "Hand off complex tasks and keep chatting. Stella works in the background, asks questions when stuck, and notifies you when done. Research, analysis, multi-step workflows.",
    // Index - Capabilities
    capabilitiesTitle: "Built-in capabilities",
    capabilitiesSubtitle: "Everything you need, nothing you don't.",
    capSkills: "Skills",
    capSkillsDesc:
      "Installable playbooks that teach Stella new workflows. Browse registries or write your own.",
    capReading: "Reading assistant",
    capReadingDesc:
      "Save articles, subscribe to RSS feeds, get daily digests. Your personal library that summarizes everything.",
    capEmail: "Email",
    capEmailDesc:
      "Read, send, and manage email through IMAP/SMTP. Stella handles your inbox from the conversation.",
    capVault: "Secrets vault",
    capVaultDesc:
      "Store API keys and tokens encrypted. Available at runtime, never exposed in conversations.",
    capOAuth: "OAuth connections",
    capOAuthDesc:
      "Connect GitHub, Google, and other services. Stella acts on your behalf with proper authorization.",
    capPlugins: "Plugins",
    capPluginsDesc:
      "Extend with custom code. Build integrations, add tools, or connect to internal services.",
    capMCP: "MCP tools",
    capMCPDesc:
      "Connect any Model Context Protocol server. Industry-standard tool integration, zero custom wiring.",
    capModels: "Multi-provider models",
    capModelsDesc:
      "Anthropic, OpenAI, or any compatible API. Switch models per agent, per task. Your keys, your choice.",
    capAgents: "Specialized agents",
    capAgentsDesc:
      "Pre-built templates for coding, research, writing, and review. Create focused agents in seconds.",
    capNotifications: "Proactive notifications",
    capNotificationsDesc:
      "Stella reaches out when something needs attention. Task done, job failed, or something you should know.",
    // Index - Footer CTA
    getStarted: "Get started in seconds",
    getStartedBody: "Two commands. No containers required.",
    getStartedAlt: "Also available via go install and direct binary download.",
    // Index - Meet Stella (used on About page)
    meetStella: "Meet Stella",
    meetStellaTitle: "A calm digital companion",
    meetStellaBody1:
      "Stella is more than a tool — she is a quiet, trustworthy assistant designed for the long run. She remembers your context, connects your workflows across devices, and stays reliably present without getting in the way.",
    meetStellaBody2:
      "Built with real warmth and digital precision. Local-first, memory-aware, and always composed.",
    learnMoreAboutStella: "Learn more about Stella",
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
    heroTitle1: "你的智能助手",
    heroTitle2: "开箱",
    heroTitle3: "即用",
    heroDescription:
      "Stella 记住每一次对话，连接你的聊天工具，替你处理日常事务。一个二进制文件，你的机器，你的数据。",
    readTheDocs: "阅读文档",
    sourceOnGithub: "GitHub 源码",
    memoryLabel: "持久记忆",
    memoryTitle: "她替你记住一切",
    memoryBody: "随时问起上周的决策或几个月前的细节。Stella 保持完整的上下文，在你需要时随时调用。",
    conversationLabel: "对话",
    featuresTitle: "Stella 的独特之处",
    feature1Title: "记住一切",
    feature1Body:
      "对话无限增长。Stella 在后台压缩旧的上下文，但不会丢失任何细节。随时询问你讨论过的任何内容。",
    feature2Title: "跨渠道工作",
    feature2Body:
      "Telegram、QQ、飞书、微信或 Web UI。所有渠道共享同一份记忆。在手机上开始，在电脑上继续。",
    feature3Title: "运行在你的机器上",
    feature3Body:
      "单一二进制文件，SQLite，你的 API 密钥。数据不会离开你的网络。支持 Homebrew、Docker 或直接下载。",
    feature4Title: "你不在时也在工作",
    feature4Body:
      "安排提醒、定期检查、RSS 摘要和自动化任务。Stella 持续运行，在需要你关注时通知你。",
    feature5Title: "异步智能体",
    feature5Body:
      "交出复杂任务，继续聊天。Stella 在后台工作，遇到问题会询问你，完成后通知你。研究、分析、多步骤工作流。",
    capabilitiesTitle: "内置能力",
    capabilitiesSubtitle: "你需要的都有，不需要的都没有。",
    capSkills: "技能",
    capSkillsDesc: "可安装的工作流剧本，教 Stella 学习新流程。浏览注册表或自己编写。",
    capReading: "阅读助手",
    capReadingDesc: "保存文章、订阅 RSS 源、获取每日摘要。你的个人图书馆，自动总结一切。",
    capEmail: "邮件",
    capEmailDesc: "通过 IMAP/SMTP 阅读、发送和管理邮件。Stella 在对话中处理你的收件箱。",
    capVault: "密钥保险库",
    capVaultDesc: "加密存储 API 密钥和令牌。运行时可用，对话中不会暴露。",
    capOAuth: "OAuth 连接",
    capOAuthDesc: "连接 GitHub、Google 等服务。Stella 以你的身份操作，具有正确的授权。",
    capPlugins: "插件",
    capPluginsDesc: "用自定义代码扩展。构建集成、添加工具或连接内部服务。",
    capMCP: "MCP 工具",
    capMCPDesc: "连接任何 Model Context Protocol 服务器。行业标准工具集成，零自定义配线。",
    capModels: "多供应商模型",
    capModelsDesc: "Anthropic、OpenAI 或任何兼容 API。按智能体、按任务切换模型。你的密钥，你做主。",
    capAgents: "专业智能体",
    capAgentsDesc: "预建编程、研究、写作和审查模板。几秒钟内创建专注的智能体。",
    capNotifications: "主动通知",
    capNotificationsDesc: "需要你关注时 Stella 会主动联系你。任务完成、作业失败或你应该知道的事。",
    getStarted: "几秒钟即可开始",
    getStartedBody: "两条命令，无需容器。",
    getStartedAlt: "也支持 go install 和直接下载二进制文件。",
    meetStella: "认识 Stella",
    meetStellaTitle: "一位沉静的数字伙伴",
    meetStellaBody1:
      "Stella 不仅仅是一个工具——她是一位安静、值得信赖的助手，为长期陪伴而设计。她记住你的上下文，连接你跨设备的工作流，始终可靠地陪伴而不打扰。",
    meetStellaBody2: "融合真实温度与数字精确。本地优先、记忆感知、始终沉着。",
    learnMoreAboutStella: "了解更多关于 Stella",
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
