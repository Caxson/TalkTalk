// Anime renderer: a VRM model (three.js + @pixiv/three-vrm). Most models
// have only VRM's five vowel shapes, so lip-sync is mapped onto them with
// VMagicMirror's ARKit→VRM formula (MIT). "Perfect Sync" models carry all
// 52 ARKit shapes as custom expressions and are driven with them directly,
// which keeps the micro-expressions the vowels throw away. Hair and
// clothes move by the model's spring bones; an idle loop keeps the body
// alive; the eyes hold contact while listening and drift up while thinking.

import * as THREE from "three";
import { GLTFLoader } from "three/addons/loaders/GLTFLoader.js";
import { perfectSync, vowels } from "./rig.js";

// Head-and-shoulders still: at 22° the frame spans ~0.43 m vertically at
// 1.1 m, from the top of the hair (head bone + ~0.2 m) to the chest.
const FRAMING = { fov: 22, distance: 1.1, above: 0.02, aim: 0.02 }; // metres around the head bone
const LOOK = { drift: 0.035, thinkUp: 0.18, thinkSide: 0.12, follow: 0.28 }; // metres at the camera

export async function mount(avatar, info) {
  document.body.classList.add("avatar-vrm");
  const [{ VRMLoaderPlugin, VRMUtils }, { VRMAnimationLoaderPlugin, createVRMAnimationClip }] = await Promise.all([
    import(info.renderer),
    import(info.renderer.replace("three-vrm.module", "three-vrm-animation.module")),
  ]);

  const host = avatar.el.canvas;
  const renderer = new THREE.WebGLRenderer({ antialias: true, alpha: true });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
  renderer.outputColorSpace = THREE.SRGBColorSpace;
  host.append(renderer.domElement);

  const scene = new THREE.Scene();
  const camera = new THREE.PerspectiveCamera(FRAMING.fov, 1, 0.1, 20);
  scene.add(new THREE.HemisphereLight(0xffffff, 0x3a3836, 1.6));
  const key = new THREE.DirectionalLight(0xffffff, 1.4);
  key.position.set(0.6, 1.4, 1.2);
  scene.add(key);

  const loader = new GLTFLoader();
  loader.register((p) => new VRMLoaderPlugin(p));
  loader.register((p) => new VRMAnimationLoaderPlugin(p));
  const gltf = await loader.loadAsync(info.model, (e) => e.total && avatar.progress(e.loaded / e.total));
  const vrm = gltf.userData.vrm;
  VRMUtils.rotateVRM0(vrm); // no-op for VRM 1.0
  VRMUtils.removeUnnecessaryVertices(gltf.scene);
  vrm.scene.traverse((o) => { o.frustumCulled = false; });
  scene.add(vrm.scene);

  const mixer = new THREE.AnimationMixer(vrm.scene);
  if (info.animation) {
    const anim = await loader.loadAsync(info.animation);
    const vrma = anim.userData.vrmAnimations?.[0];
    if (vrma) mixer.clipAction(createVRMAnimationClip(vrma, vrm)).play();
  }
  mixer.update(0);
  vrm.update(0);

  // Frame head and shoulders around the head bone.
  const headPos = new THREE.Vector3();
  vrm.humanoid.getNormalizedBoneNode("head").getWorldPosition(headPos);
  camera.position.set(headPos.x, headPos.y + FRAMING.above, headPos.z + FRAMING.distance);
  camera.lookAt(headPos.x, headPos.y + FRAMING.aim, headPos.z);

  // Gaze target: in front of the camera, nudged by small saccades.
  const gaze = new THREE.Object3D();
  scene.add(gaze);
  if (vrm.lookAt) vrm.lookAt.target = gaze;
  let saccade = new THREE.Vector2(), nextSaccade = 0;

  const resize = () => {
    const { clientWidth: w, clientHeight: h } = host;
    if (!w || !h) return;
    renderer.setSize(w, h, false);
    renderer.domElement.style.width = "100%";
    renderer.domElement.style.height = "100%";
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
  };
  new ResizeObserver(resize).observe(host);
  resize();

  const em = vrm.expressionManager;
  const arkit = perfectSync(em.expressions.map((x) => x.expressionName));
  if (arkit) avatar.el.take.textContent += " · ARKit 52";
  const head = vrm.humanoid.getNormalizedBoneNode("head");
  // Behavior offsets on the head, as applied last frame. They are taken
  // off before the animation updates and put back after, so they never
  // accumulate, whether or not the idle clip animates the head.
  const applied = { pitch: 0, yaw: 0 };
  const clock = new THREE.Clock();
  const draw = () => {
    const dt = Math.min(clock.getDelta(), 0.1);
    const e = avatar.expression();
    const b = avatar.act();
    if (arkit) {
      // The behavior's smile answers on the face's own smile shapes; the
      // eyes are left to lookAt so camera following stays in one place.
      for (const side of ["Left", "Right"]) e[`mouthSmile${side}`] = Math.max(e[`mouthSmile${side}`] ?? 0, b.smile * 0.6);
      for (const [k, w] of Object.entries(e)) {
        const name = !k.startsWith("eyeLook") && arkit.get(k.toLowerCase());
        if (name) em.setValue(name, w);
      }
      avatar.mouth = e.jawOpen ?? 0;
    } else {
      em.setValue("relaxed", Math.min(1, b.smile * 0.85));
      const v = vowels(e);
      em.setValue("aa", v.aa);
      em.setValue("ih", v.ih);
      em.setValue("ou", v.ou);
      em.setValue("blinkLeft", e.eyeBlinkLeft ?? 0);
      em.setValue("blinkRight", e.eyeBlinkRight ?? 0);
      avatar.mouth = v.aa;
    }

    const now = performance.now();
    if (now > nextSaccade) {
      const k = LOOK.drift * b.saccadeScale;
      saccade.set((Math.random() - 0.5) * 2 * k, (Math.random() - 0.5) * k);
      nextSaccade = now + 900 + Math.random() * 2200;
    }
    const thinking = avatar.state === "thinking";
    gaze.position.set(
      camera.position.x + saccade.x + b.gaze.x * LOOK.follow + (thinking ? LOOK.thinkSide : 0),
      camera.position.y + saccade.y + b.gaze.y * LOOK.follow * 0.6 + (thinking ? LOOK.thinkUp : 0),
      camera.position.z,
    );

    head.rotation.x -= applied.pitch;
    head.rotation.y -= applied.yaw;
    mixer.update(dt);
    // Behavior rides on top of the idle animation: nod, and turn the head
    // a little toward the person (the eyes do most of the following).
    applied.pitch = b.nod;
    applied.yaw = b.gaze.x * 0.12;
    head.rotation.x += applied.pitch;
    head.rotation.y += applied.yaw;
    avatar.headYaw = head.rotation.y;
    vrm.update(dt);
    renderer.render(scene, camera);
    requestAnimationFrame(draw);
  };
  requestAnimationFrame(draw);
}
