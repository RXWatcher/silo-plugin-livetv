import { useEffect, useRef } from 'react';
import mpegts from 'mpegts.js';

interface Props {
  src: string;
}

// MpegtsPlayer drives an MPEG-TS feed via mpegts.js (MSE-based). On Safari
// — which can't play MPEG-TS natively — playback will fail; the fallback to
// <video src=...> here is mostly a courtesy. The recommended setup is to
// detect Safari upstream and prefer the HLS variant.
export function MpegtsPlayer({ src }: Props) {
  const ref = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    const v = ref.current;
    if (!v) return;

    if (mpegts.getFeatureList().mseLivePlayback) {
      const player = mpegts.createPlayer({ type: 'mpegts', isLive: true, url: src });
      player.attachMediaElement(v);
      player.load();
      // play() may return void or Promise<void> depending on the runtime
      // (typings declare a union). When it's a promise, swallow rejections
      // from autoplay-blocked browsers so the page still loads cleanly —
      // the user can hit the visible <video controls> button.
      const playResult = player.play();
      if (playResult && typeof (playResult as Promise<void>).then === 'function') {
        (playResult as Promise<void>).catch(() => {});
      }
      return () => {
        player.pause();
        player.unload();
        player.detachMediaElement();
        player.destroy();
      };
    }

    v.src = src;
    return undefined;
  }, [src]);

  return <video ref={ref} controls autoPlay playsInline className="aspect-video w-full bg-black" />;
}
