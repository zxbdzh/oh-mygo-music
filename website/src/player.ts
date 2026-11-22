import { createSignal } from 'solid-js'

// 保留歌曲元数据供演示界面展示，实际音频播放已关闭。
export const track = {
  title: '春日影',
  artist: 'MyGO!!!!!',
  album: '迷跡波',
  duration: 281,
}

const [playing, setPlaying] = createSignal(false)
const [position, setPosition] = createSignal(62)
const [liked, setLiked] = createSignal(false)
export { playing, position, liked, setLiked }

export function play() {
  setPlaying(false)
}

export function pause() {
  setPlaying(false)
}

export function toggle() {
  setPlaying(false)
}

export function seek(fraction: number) {
  const p = Math.max(0, Math.min(1, fraction)) * track.duration
  setPosition(p)
  if (audio) audio.currentTime = p
}

export function clock(seconds: number) {
  const s = Math.floor(seconds)
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}
