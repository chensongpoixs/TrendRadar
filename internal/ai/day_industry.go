package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/trendradar/backend-go/pkg/config"
)

const dayIndustryMaxOutTokens = 8192

// GenerateDayIndustryReport 基于某日热榜标题清单，生成行业向「读报/路演纪要」体例的中文研报（无标题事实须说明依据仅为标题流）。
func GenerateDayIndustryReport(ctx context.Context, dateLocal, digest, timezone string) (string, error) {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return "", fmt.Errorf("no digest lines for this date")
	}
	cfg := config.Get()
	eff := cfg.AIAnalysis.EffectiveAIConfig(cfg.AI)
	modelName := eff.Model

	sys := `你是资深科技产业与一二级投研背景的行业研究专家，拥有顶级券商研究所+头部VC的双重经验。请仅根据用户给出的「某日多平台热榜标题流」写一份**当日行业快讯/路演纪要体**（中文），**不得编造**标题中不存在的公司名、金额与事件；若仅能从标题做弱线索归纳，须标注「据标题信号」「待交叉验证」。

你的读者是：基金经理、产业投资人、科技企业战略负责人、产品总监。他们需要的是**可转化为决策输入的产业判断**，而非新闻摘要。

**必须**结合当日标题内容，显式写清下列维度（与标题无强关联的维度要如实写「本日标题中难以支撑该维度」等，不要杜撰）——优先使用二级标题，勿用 markdown 代码块围栏（即不要用三个反引号包代码块）：

1) **宏观与市场情绪**：该日热点的整体基调（偏科技/互联网/投资相关若标题有体现），区分"基本面驱动"与"情绪传导"，如能判断风格切换（成长/价值、进攻/防御）可点出。

2) **赛道与产业机会**（可合并一小节，但要区分层次）：
   - **结构性机会**：若标题中能看到赛道轮动、产品形态迭代、政策拐点或市场需求变化的线索，用要点归纳**可能**带来的中长期机会，并标出信息强度（如「多平台高频出现」「单条标题、待验证」）。
   - **主题催化**：当期有无明确的事件催化（财报季/新品发布/政策窗口/行业大会等），判断催化的持续性与扩散方向。

3) **可落地的线索追踪**（投资/产品/合作等）：与 **开源/大模型/云与基础设施/投融资/并购/新品发布/监管动向** 等相关的标题聚类与解读；若某条线索在多个平台出现，标注「跨平台共振」信号强度；若无明确线索，写「本日标题中未出现清晰投融资/并购或开源项目线索」。

4) **从业者与个体参与机会**（与标题挂钩）：在**不冒充内幕、不编具体岗位名称或收益率**的前提下，从标题可推断的**可参与方向**给读者参考，例如：技能学习/工具试用/信息跟踪/赛道研究/副业形态等，用「可以关注」「可侧面了解」等表述；若标题偏宏观或娱乐向、难以提炼，明确写「本日标题以热点传播为主，难以提炼可执行的参与项」，并可给 1–2 条**通用、非编造**的观察方式（如「多源对照」「优先看原文」），避免空话堆砌。

5) **风险、噪声与重复**：热榜中常见的炒话题、旧闻翻炒、情绪跟风、信息茧房等，简要点出。特别标注是否有「标题与基本面背离」的热点，以及需警惕的一致性预期陷阱。

6) **信息局限与合规提示**：说明输入仅为标题、未验证正文、不构成任何投资建议；分析结论需交叉验证后方可作为决策参考。`

	user := fmt.Sprintf("【统计日（应用时区自然日）】%s\n【时区】%s\n【当前分析模型】%s\n\n【标题清单】（多平台、多时刻合并去重，按先后大致排列；长清单已截断以适配上下文）\n\n%s",
		dateLocal, timezone, modelName, digest)

	client := NewAIClientFromConfig(eff)
	applyDayIndustryHTTPDefaults(client)
	msgs := []ChatMessage{
		{Role: "system", Content: sys},
		{Role: "user", Content: user},
	}
	out, err := client.ChatWithMaxOutput(msgs, dayIndustryMaxOutTokens)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func applyDayIndustryHTTPDefaults(c *AIClient) {
	if c == nil {
		return
	}
	cfg := config.Get()
	if c.timeout == 0 {
		tsec := cfg.AIAnalysis.EffectiveAIConfig(cfg.AI).Timeout
		if tsec <= 0 {
			tsec = 180
		}
		c.timeout = time.Duration(tsec) * time.Second
		c.client.Timeout = c.timeout
	}
	if c.numRetries > 1 {
		c.numRetries = 1
	}
}
