import { useEffect, useRef } from 'react';
import Hls from 'hls.js';

interface Props {
  src: string;
}

// HlsPlayer renders an HTML <video> wired through hls.js when MSE is
// available (Chrome/Firefox/Edge) and falls back to the browser's native
// HLS support on Safari/iOS. The destroy() in cleanup is critical — without
// it, hls.js keeps fetching segments after the player unmounts and pegs the
// network.
export function HlsPlayer({ src }: Props) {
  const ref = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    const v = ref.current;
    if (!v) return;

    if (Hls.isSupported()) {
      const hls = new Hls({ liveDurationInfinity: true });
      hls.loadSource(src);
      hls.attachMedia(v);
      return () => {
        hls.destroy();
      };
    }

    // Native HLS on Safari/iOS.
    v.src = src;
    return undefined;
  }, [src]);

  return <video ref={ref} controls autoPlay playsInline className="aspect-video w-full bg-black" />;
}
