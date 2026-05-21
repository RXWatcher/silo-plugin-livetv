import { useParams } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/api/client';
import { HlsPlayer } from '@/player/HlsPlayer';
import { MpegtsPlayer } from '@/player/MpegtsPlayer';
import { NowNextPanel } from '@/player/NowNextPanel';

// PlayerPage is the watch surface. Two parallel queries run on mount:
//   - api.startStream() POSTs to mint a stream session; the response carries
//     the playback URL and the host attaches a session cookie to the browser
//     so subsequent <video>/HLS requests authenticate transparently.
//   - api.channel() fetches the channel metadata for the title strip and
//     to seed the NowNext panel.
//
// Stream selection: the playback URL ends in .m3u8 for HLS and .ts for raw
// MPEG-TS (see internal/streamproxy). We branch on the URL rather than on
// channel.upstream_kind because the proxy may transcode/repackage.
export function PlayerPage() {
  const { channelId = '' } = useParams();

  const streamQuery = useQuery({
    queryKey: ['stream', channelId],
    queryFn: () => api.startStream(channelId),
    refetchOnWindowFocus: false,
    staleTime: Infinity,
    retry: 0,
    enabled: !!channelId,
  });

  const channelQuery = useQuery({
    queryKey: ['channel', channelId],
    queryFn: () => api.channel(channelId),
    enabled: !!channelId,
  });

  if (!channelId) {
    return <div className="p-6 text-sm text-[color:var(--color-muted-foreground)]">No channel.</div>;
  }

  if (streamQuery.isPending) {
    return <div className="p-6 text-sm text-[color:var(--color-muted-foreground)]">Starting stream…</div>;
  }
  if (streamQuery.isError) {
    return (
      <div className="rounded-md border border-red-900/40 bg-red-950/30 p-4 text-sm text-red-300">
        Stream unavailable: {(streamQuery.error as Error).message}
      </div>
    );
  }
  if (!streamQuery.data) return null;
  if (channelQuery.isPending) {
    return <div className="p-6 text-sm text-[color:var(--color-muted-foreground)]">Loading channel…</div>;
  }
  if (!channelQuery.data) return null;

  const url = streamQuery.data.playback_url;
  // Split a query-string off the path so an .m3u8?token=foo URL still
  // matches the suffix check.
  const path = url.split('?')[0] ?? url;
  const isHLS = path.endsWith('.m3u8');

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-[2fr_1fr]">
      <div>
        <div className="overflow-hidden rounded-lg border border-[color:var(--color-border)] bg-black">
          {isHLS ? <HlsPlayer src={url} /> : <MpegtsPlayer src={url} />}
        </div>
        <h1 className="mt-3 text-lg font-medium">{channelQuery.data.display_name}</h1>
        {channelQuery.data.current_program ? (
          <p className="text-sm text-[color:var(--color-muted-foreground)]">
            Now: {channelQuery.data.current_program.title}
          </p>
        ) : null}
      </div>
      <NowNextPanel channel={channelQuery.data} />
    </div>
  );
}
