// 人设正文的写法检查：纯客户端，不请求后端，也不拦保存。
//
// 人设编辑框旁边有一排开关——自称、句尾语气词、动作描写、分条和长短，都是运行时
// 单独拼进提示词的。用户不知道这件事，会把「每句话都以『喵』结尾」「必须自称本喵」
// 「不要用 Markdown」直接写进正文，于是同一件事被规定两遍：正文说每句都要带，
// 句尾语气词开关说按当下语气挑。两份指令打架，模型只能挑一个听，输出就忽好忽坏，
// 而用户改开关不见效，只会以为开关坏了。
//
// 所以这里只提示、不改写：正文是用户写的，我们只负责告诉他这条已经有开关管了。
// 判断靠正则，必然有误伤也必然有漏网——所以文案一律是「这条由 X 控制」，
// 而不是「你写错了」，提示也不阻塞保存。

export interface PersonaLintOptions {
  /** 句尾语气词的当前值，留给规则判断用。 */
  sentenceEnders?: string;
  /** 自称的当前值，留给规则判断用。 */
  selfReference?: string;
  /** 动作描写开关。关着的时候正文里的括号动作不会生效，才需要提醒。 */
  actionDescriptionEnabled?: boolean;
}

export type PersonaLintCode = "sentence-enders" | "self-reference" | "action-description" | "formatting";

export interface PersonaLintWarning {
  code: PersonaLintCode;
  message: string;
  /** 命中的原文片段，界面上引一句给用户定位。 */
  match: string;
}

interface Rule {
  code: PersonaLintCode;
  pattern: RegExp;
  message: string;
  /** 返回 false 表示这条规则在当前开关状态下不检查。 */
  enabled?: (options: PersonaLintOptions) => boolean;
}

// 一条规则最多报一次：正文里把同一件事翻来覆去说三遍，提示也跟着刷三条的话，
// 真正不同的那几条会被淹掉。同一类下分成几条规则，是因为它们该说的话不一样。
const RULES: Rule[] = [
  {
    code: "sentence-enders",
    pattern: /每句(话)?(都)?(以|用|加).{1,6}(结尾|收尾)|句句(都)?带|每句都要/,
    message: "句尾语气词由下面的「句尾语气词」控制，写死「每句都要」会和它打架。"
  },
  {
    code: "sentence-enders",
    pattern: /(句末)?不(加|打)(句号|标点)/,
    message: "句末标点跟着句尾语气词和回复格式走，正文里禁掉标点会被两边来回改。"
  },
  {
    code: "self-reference",
    pattern: /必须自称|每句(都)?自称/,
    message: "自称由下面的「自称」控制，正文里写死会盖掉那里填的值。"
  },
  {
    code: "action-description",
    pattern: /（[^）]{1,12}）|\*[^*\n]{1,10}\*/,
    message: "括号动作由下面的「动作描写」开关控制，开关关着时这类写法不会生效。",
    enabled: options => options.actionDescriptionEnabled === false
  },
  {
    code: "formatting",
    pattern: /不(要|用|使用)\s*Markdown|纯文本|分条|不超过\s*\d+\s*字|emoji|表情符号|<dianabr>|\[diana-msg\]/i,
    message: "分条、长短和消息格式由「接话设置」和回复长度统一控制，正文里再规定会互相盖。"
  }
];

/**
 * personaLint 检查一段人设正文里有没有「本该由开关管」的规定。
 *
 * 纯函数：同样的输入永远给同样的结果，不读全局状态、不发请求、不改传进来的对象。
 */
export function personaLint(text: string, options: PersonaLintOptions = {}): PersonaLintWarning[] {
  const source = (text ?? "").trim();
  if (!source) return [];
  const warnings: PersonaLintWarning[] = [];
  for (const rule of RULES) {
    if (rule.enabled && !rule.enabled(options)) continue;
    const hit = rule.pattern.exec(source);
    if (!hit) continue;
    warnings.push({ code: rule.code, message: rule.message, match: hit[0] });
  }
  return warnings;
}
