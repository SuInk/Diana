// 接管模式下，「这个角色怎么说话」那几段运行时一律不注入，界面上对应的控件也跟着
// 藏起来：自称、句尾语气词、动作描写、语气跟随时段。用户选了接管，就是说这些他自己
// 在人设正文里写。
//
// 判据只有档位一个。早先试过按正文里的段头逐段判断（正文写了「自称与语气词：」就
// 认为它接管了自称），那条路要拿字符串去匹配用户写的散文——段头少一个标点、换一种
// 写法就悄悄改变行为，而且界面和运行时各匹配一次，还可能给出不同答案。档位是用户
// 明确选的，不用猜，两边也不会打架。
//
// 不跟着关的是另一类：接话设置、回复长度与分条、平台差异、好感度语气。它们不是
// 「怎么说话」，是何时开口、怎么投递和运行时上下文，正文写死了也不作数。

/** 和后端 PersonaMode 同一套取值；空值按 fill 处理。 */
export type PersonaMode = "fill" | "own";

/** personaOwnsVoice 报告这一档是不是由人设正文全权负责「怎么说话」。 */
export function personaOwnsVoice(mode: PersonaMode | undefined): boolean {
  return mode === "own";
}

/** 一项被正文接管的设置：界面上它的控件已经藏起来，这里交代它的去向。 */
export interface PersonaOwnedNotice {
  /** 这一项在界面上原来叫什么。 */
  label: string;
  /** 控件里还存着、但当前不生效的值；没存值就是空串。 */
  staleValue: string;
}

/**
 * personaOwnedNotices 列出当前被正文接管的几项，供界面汇总成一行。
 *
 * 带上 staleValue 是因为控件藏起来之后，它存的值还在配置里：藏掉一个填过「本喵」
 * 的输入框，那个值就既看不见也改不掉，只在某天切回填空题时突然复活。说出来才不算
 * 隐形状态。开关只在「开着」时才值得提——关着和默认一致，说出来只是噪音。
 *
 * 纯函数，不读全局状态也不改传进来的对象。
 */
export function personaOwnedNotices(
  mode: PersonaMode | undefined,
  settings: {
    selfReference?: string;
    sentenceEnders?: string;
    actionDescriptionEnabled?: boolean;
    daypartToneEnabled?: boolean;
  }
): PersonaOwnedNotice[] {
  if (!personaOwnsVoice(mode)) return [];
  return [
    { label: "自称", staleValue: (settings.selfReference ?? "").trim() },
    { label: "句尾语气词", staleValue: (settings.sentenceEnders ?? "").trim() },
    { label: "动作描写", staleValue: settings.actionDescriptionEnabled ? "已开启" : "" },
    { label: "语气跟随时段", staleValue: settings.daypartToneEnabled ? "已开启" : "" }
  ];
}
