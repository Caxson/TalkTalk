package lipsync

// Names lists the 52 ARKit blendshapes in the model's output order
// (alphabetical, per the wav2arkit model card).
var Names = [Channels]string{
	"browDownLeft", "browDownRight", "browInnerUp", "browOuterUpLeft", "browOuterUpRight",
	"cheekPuff", "cheekSquintLeft", "cheekSquintRight",
	"eyeBlinkLeft", "eyeBlinkRight", "eyeLookDownLeft", "eyeLookDownRight",
	"eyeLookInLeft", "eyeLookInRight", "eyeLookOutLeft", "eyeLookOutRight",
	"eyeLookUpLeft", "eyeLookUpRight", "eyeSquintLeft", "eyeSquintRight",
	"eyeWideLeft", "eyeWideRight",
	"jawForward", "jawLeft", "jawOpen", "jawRight",
	"mouthClose", "mouthDimpleLeft", "mouthDimpleRight", "mouthFrownLeft", "mouthFrownRight",
	"mouthFunnel", "mouthLeft", "mouthLowerDownLeft", "mouthLowerDownRight",
	"mouthPressLeft", "mouthPressRight", "mouthPucker", "mouthRight",
	"mouthRollLower", "mouthRollUpper", "mouthShrugLower", "mouthShrugUpper",
	"mouthSmileLeft", "mouthSmileRight", "mouthStretchLeft", "mouthStretchRight",
	"mouthUpperUpLeft", "mouthUpperUpRight",
	"noseSneerLeft", "noseSneerRight", "tongueOut",
}
