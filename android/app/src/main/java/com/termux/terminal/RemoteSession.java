package com.termux.terminal;

/**
 * Drives Termux's {@link TerminalSession} from a byte stream instead of a local
 * process. TerminalView only accepts a TerminalSession, which is final and
 * normally forks a subprocess on a pty; this class sits in the same package so
 * it can reach the session's package-private state: it installs the emulator
 * itself, never starts the subprocess, feeds the emulator's input queue with
 * what the remote session produced, and drains what the user typed (the
 * session's output queue) into a {@link Sink}.
 */
public final class RemoteSession {

    /** Receives what the user typed, as raw bytes. */
    public interface Sink {
        void write(byte[] data, int offset, int count);
    }

    private static final int MSG_NEW_INPUT = 1; // TerminalSession.MSG_NEW_INPUT

    public final TerminalSession session;
    private volatile boolean closed;

    public RemoteSession(TerminalSessionClient client, Sink sink, int columns, int rows, int transcriptRows) {
        session = new TerminalSession("/", "/", new String[0], new String[0], transcriptRows, client);
        session.mEmulator = new TerminalEmulator(session, columns, rows, 10, 20, transcriptRows, client);
        // write() only queues input while the session looks alive.
        session.mShellPid = 1;
        Thread typed = new Thread("RemoteSession-input") {
            @Override
            public void run() {
                byte[] buffer = new byte[4096];
                while (!closed) {
                    int n = session.mTerminalToProcessIOQueue.read(buffer, true);
                    if (n == -1) return;
                    sink.write(buffer, 0, n);
                }
            }
        };
        typed.setDaemon(true);
        typed.start();
    }

    /** Hands remote output to the emulator; the emulator parses it on the main thread. Any thread. */
    public void feed(byte[] data) {
        int offset = 0;
        while (offset < data.length && !closed) {
            // The queue holds 4096 bytes and the main thread drains it once per message.
            int n = Math.min(4096, data.length - offset);
            if (!session.mProcessToTerminalIOQueue.write(data, offset, n)) return;
            session.mMainThreadHandler.sendEmptyMessage(MSG_NEW_INPUT);
            offset += n;
        }
    }

    public void close() {
        closed = true;
        session.mTerminalToProcessIOQueue.close();
        session.mProcessToTerminalIOQueue.close();
    }
}
