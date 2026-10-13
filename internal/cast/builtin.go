package cast

// Builtin is the default roster, matching what `make assets` downloads.
// Every face is driven by the same ARKit stream (trained on real people);
// the renderers differ in how much of it they can show.
func Builtin() []Character {
	return []Character{
		{
			ID: "mio", Name: "Mio", Tagline: "三维二次元 · 元气少女",
			Kind: KindVRM, Model: "avatars/AvatarSample_B.vrm", Animation: "avatars/idle_loop.vrma",
			Voice:   "longhua_v3",
			Persona: "你的名字叫 Mio，是个开朗、有点俏皮的女孩，说话轻快，爱接梗，偶尔撒个娇。",
		},
		{
			ID: "hiyori", Name: "Hiyori", Tagline: "平面二次元 · 软萌",
			Kind: KindLive2D, Model: "avatars/hiyori/Hiyori.model3.json",
			Voice:   "longanhuan_v3",
			Persona: "你的名字叫日和（Hiyori），是个软萌、好奇心很重的女孩，说话带点小雀跃，喜欢追问细节。",
			Credit:  "This content uses sample data owned and copyrighted by Live2D Inc.",
		},
		{
			ID: "ya", Name: "Ya", Tagline: "真人写实 · 知性",
			Kind: KindLAM, Model: "avatars/status.zip",
			Voice:   "longanwen_v3",
			Persona: "你的名字叫雅，说话沉稳、知性、有分寸，像一位温和的朋友。",
		},
	}
}
