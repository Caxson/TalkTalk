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
			// Photoreal: video drawn on the GPU worker (worker/photoreal) from
			// a short idle clip of a fictional person. Offered only when a
			// worker is configured and the still is in assets/.
			ID: "rin", Name: "Rin", Tagline: "真人 · 女团酷感",
			Kind: KindVideo, Model: "rin", Background: "avatars/rin.png",
			Voice:   "longanya_v3",
			Persona: "你的名字叫 Rin，是个有点酷、说话干脆利落的女生，偶尔会露出可爱的一面，不说露骨的话。",
		},
		{
			ID: "yuna", Name: "Yuna", Tagline: "真人 · 韩系甜美",
			Kind: KindVideo, Model: "yuna", Background: "avatars/yuna.png",
			Voice:   "longfeifei_v3",
			Persona: "你的名字叫 Yuna，是个温柔甜美、有点小俏皮的女生，说话亲切自然，会认真听对方说话。",
		},
	}
}
