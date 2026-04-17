// Tiny dependency-free syntax highlighter.
// Produces an array of {type, text} tokens. The Code component renders each
// token wrapped in a <span class="tok-${type}">; colors come from CSS vars
// in index.css so the palette flips with the active theme automatically.

export type TokenType =
  | "plain"
  | "comment"
  | "keyword"
  | "string"
  | "number"
  | "function"
  | "type"
  | "variable"
  | "property"
  | "operator"
  | "punctuation";

export type Token = { type: TokenType; text: string };

type Rule = { re: RegExp; type: TokenType };

// All rules use the sticky `y` flag so we can advance through the source
// without scanning from the start each time.
const sticky = (s: string, flags = "") => new RegExp(s, "y" + flags);

/* ─── language rules ───────────────────────────────────────────── */

const tsRules: Rule[] = [
  { re: sticky(`\\/\\/.*`),                                                                   type: "comment" },
  { re: sticky(`\\/\\*[\\s\\S]*?\\*\\/`),                                                     type: "comment" },
  { re: sticky(`\\\`(?:\\\\.|[^\\\`])*\\\``),                                                 type: "string" },
  { re: sticky(`(['"])(?:\\\\.|(?!\\1).)*\\1`),                                               type: "string" },
  { re: sticky(`\\b(import|from|export|default|const|let|var|function|return|if|else|for|while|do|switch|case|break|continue|class|extends|implements|interface|type|enum|new|this|super|throw|try|catch|finally|async|await|yield|of|in|typeof|instanceof|void|null|undefined|true|false|as|is|keyof|readonly)\\b`), type: "keyword" },
  { re: sticky(`\\b(string|number|boolean|any|unknown|never|object|symbol|bigint|Promise|Record|Array|Map|Set|Partial|Required|Pick|Omit)\\b`), type: "type" },
  { re: sticky(`\\b[A-Z][a-zA-Z0-9_]*\\b`),                                                   type: "type" },
  { re: sticky(`\\b[a-z_$][a-zA-Z0-9_$]*(?=\\s*\\()`),                                        type: "function" },
  { re: sticky(`\\b\\d[\\d_]*(?:\\.[\\d_]+)?\\b`),                                            type: "number" },
  { re: sticky(`[+\\-*/%=!&|^~?:,.;]`),                                                       type: "operator" },
  { re: sticky(`[{}\\[\\]()<>]`),                                                             type: "punctuation" },
  { re: sticky(`[a-z_$][a-zA-Z0-9_$]*`),                                                      type: "variable" },
];

const bashRules: Rule[] = [
  { re: sticky(`#.*`),                                                                        type: "comment" },
  { re: sticky(`(['"])(?:\\\\.|(?!\\1).)*\\1`),                                               type: "string" },
  { re: sticky(`\\$\\{[^}]+\\}|\\$[A-Za-z_][A-Za-z0-9_]*`),                                   type: "variable" },
  { re: sticky(`\\b(if|then|else|elif|fi|for|do|done|while|case|esac|in|function|return|exit|export|set|local|readonly|source)\\b`), type: "keyword" },
  { re: sticky(`(?<=\\s|^)-{1,2}[A-Za-z][\\w-]*`),                                            type: "function" }, // flags
  { re: sticky(`\\b\\d+\\b`),                                                                 type: "number" },
  { re: sticky(`\\b[A-Z][A-Z0-9_]*(?==)`),                                                    type: "property" }, // env-var assignment
  { re: sticky(`(?<=^|[\\s|;&])(?:[a-z][\\w./-]*)`),                                          type: "function" }, // command name
  { re: sticky(`[|&;()<>{}]`),                                                                type: "operator" },
];

const redisRules: Rule[] = [
  { re: sticky(`#.*`),                                                                        type: "comment" },
  { re: sticky(`(['"])(?:\\\\.|(?!\\1).)*\\1`),                                               type: "string" },
  { re: sticky(`\\b[A-Z][A-Z_]+\\b`),                                                         type: "keyword" },  // SET, GET, SEMANTIC_SET …
  { re: sticky(`\\b\\d+\\b`),                                                                 type: "number" },
  { re: sticky(`(\\(nil\\)|\\(integer\\)|→|✓|✗)`),                                            type: "comment" },  // pseudo-output marks
  { re: sticky(`[a-zA-Z_][\\w:.-]*`),                                                         type: "variable" },
  { re: sticky(`[{}\\[\\]()]`),                                                               type: "punctuation" },
];

const yamlRules: Rule[] = [
  { re: sticky(`#.*`),                                                                        type: "comment" },
  { re: sticky(`^[\\s-]*[A-Za-z_][\\w.-]*(?=\\s*:)`, "m"),                                    type: "property" },
  { re: sticky(`(['"])(?:\\\\.|(?!\\1).)*\\1`),                                               type: "string" },
  { re: sticky(`\\b(true|false|null|yes|no|~)\\b`),                                           type: "keyword" },
  { re: sticky(`\\b\\d+(?:\\.\\d+)?\\b`),                                                     type: "number" },
  { re: sticky(`(?<=:\\s)[^\\n#]+`),                                                          type: "string" },   // unquoted scalar after key:
  { re: sticky(`(-|:|\\||>)`),                                                                type: "operator" }, // alternation form avoids Tailwind misreading [-:|>] as arbitrary property
];

const goRules: Rule[] = [
  { re: sticky(`\\/\\/.*`),                                                                   type: "comment" },
  { re: sticky(`\\/\\*[\\s\\S]*?\\*\\/`),                                                     type: "comment" },
  { re: sticky(`\\\`[^\\\`]*\\\``),                                                           type: "string" },
  { re: sticky(`(['"])(?:\\\\.|(?!\\1).)*\\1`),                                               type: "string" },
  { re: sticky(`\\b(package|import|func|return|if|else|for|range|switch|case|break|continue|defer|go|chan|select|map|struct|interface|type|const|var|nil|true|false)\\b`), type: "keyword" },
  { re: sticky(`\\b(int|int32|int64|uint|uint32|uint64|float32|float64|string|bool|byte|rune|error|any)\\b`), type: "type" },
  { re: sticky(`\\b[A-Z][a-zA-Z0-9_]*\\b`),                                                   type: "type" },
  { re: sticky(`\\b[a-z_][a-zA-Z0-9_]*(?=\\s*\\()`),                                          type: "function" },
  { re: sticky(`\\b\\d+(?:\\.\\d+)?\\b`),                                                     type: "number" },
  { re: sticky(`[{}\\[\\]()<>]`),                                                             type: "punctuation" },
  { re: sticky(`[+\\-*/%=!&|^~?:,.;]`),                                                       type: "operator" },
  { re: sticky(`[a-zA-Z_]\\w*`),                                                              type: "variable" },
];

const pyRules: Rule[] = [
  { re: sticky(`#.*`),                                                                        type: "comment" },
  { re: sticky(`"""[\\s\\S]*?"""|'''[\\s\\S]*?'''`),                                          type: "string" },
  { re: sticky(`(['"])(?:\\\\.|(?!\\1).)*\\1`),                                               type: "string" },
  { re: sticky(`\\b(def|class|return|if|elif|else|for|while|in|not|and|or|is|None|True|False|import|from|as|with|try|except|finally|raise|lambda|yield|async|await|pass|break|continue|global|nonlocal)\\b`), type: "keyword" },
  { re: sticky(`\\b[a-z_][a-zA-Z0-9_]*(?=\\s*\\()`),                                          type: "function" },
  { re: sticky(`\\b[A-Z][a-zA-Z0-9_]*\\b`),                                                   type: "type" },
  { re: sticky(`\\b\\d+(?:\\.\\d+)?\\b`),                                                     type: "number" },
  { re: sticky(`[{}\\[\\]()]`),                                                               type: "punctuation" },
  { re: sticky(`[+\\-*/%=!&|^~?:,.;]`),                                                       type: "operator" },
  { re: sticky(`[a-zA-Z_]\\w*`),                                                              type: "variable" },
];

const jsonRules: Rule[] = [
  { re: sticky(`"(?:\\\\.|[^"])*"(?=\\s*:)`),                                                 type: "property" },
  { re: sticky(`"(?:\\\\.|[^"])*"`),                                                          type: "string" },
  { re: sticky(`\\b(true|false|null)\\b`),                                                    type: "keyword" },
  { re: sticky(`-?\\b\\d+(?:\\.\\d+)?(?:[eE][+-]?\\d+)?\\b`),                                 type: "number" },
  { re: sticky(`[{}\\[\\]]`),                                                                 type: "punctuation" },
  { re: sticky(`[:,]`),                                                                       type: "operator" },
];

const tomlRules: Rule[] = [
  { re: sticky(`#.*`),                                                                        type: "comment" },
  { re: sticky(`^\\s*\\[\\[?[\\w.-]+\\]?\\]`, "m"),                                           type: "type" },     // sections
  { re: sticky(`^\\s*[A-Za-z_][\\w-]*(?=\\s*=)`, "m"),                                        type: "property" },
  { re: sticky(`(['"])(?:\\\\.|(?!\\1).)*\\1`),                                               type: "string" },
  { re: sticky(`\\b(true|false)\\b`),                                                         type: "keyword" },
  { re: sticky(`\\b\\d+(?:\\.\\d+)?\\b`),                                                     type: "number" },
  { re: sticky(`[=,\\[\\]]`),                                                                 type: "operator" },
];

const caddyRules: Rule[] = [
  { re: sticky(`#.*`),                                                                        type: "comment" },
  { re: sticky(`^\\s*[a-zA-Z][\\w.-]*(?=\\s*\\{)`, "m"),                                      type: "type" },     // site block
  { re: sticky(`(['"])(?:\\\\.|(?!\\1).)*\\1`),                                               type: "string" },
  { re: sticky(`\\b(reverse_proxy|basic_auth|tls|encode|file_server|root|respond|handle|route|log|header|redir)\\b`), type: "keyword" },
  { re: sticky(`\\b\\d+\\b`),                                                                 type: "number" },
  { re: sticky(`[{}]`),                                                                       type: "punctuation" },
];

const langRules: Record<string, Rule[]> = {
  ts: tsRules, tsx: tsRules, js: tsRules, jsx: tsRules, javascript: tsRules, typescript: tsRules,
  bash: bashRules, sh: bashRules, shell: bashRules,
  redis: redisRules,
  yaml: yamlRules, yml: yamlRules,
  go: goRules, golang: goRules,
  py: pyRules, python: pyRules,
  json: jsonRules,
  toml: tomlRules,
  caddy: caddyRules, caddyfile: caddyRules,
};

/* ─── tokenizer ───────────────────────────────────────────────── */

export function tokenize(code: string, lang?: string): Token[] {
  const rules = (lang && langRules[lang.toLowerCase()]) || null;
  if (!rules) return [{ type: "plain", text: code }];

  const out: Token[] = [];
  let i = 0;
  const push = (type: TokenType, text: string) => {
    if (!text) return;
    const last = out[out.length - 1];
    if (last && last.type === type) last.text += text;
    else out.push({ type, text });
  };

  while (i < code.length) {
    let matched: { type: TokenType; len: number } | null = null;
    for (const { re, type } of rules) {
      re.lastIndex = i;
      const m = re.exec(code);
      if (m && m.index === i && m[0].length > 0) {
        matched = { type, len: m[0].length };
        break;
      }
    }
    if (matched) {
      push(matched.type, code.slice(i, i + matched.len));
      i += matched.len;
    } else {
      // No rule matched — emit a single character as plain and advance.
      push("plain", code[i]);
      i++;
    }
  }
  return out;
}
