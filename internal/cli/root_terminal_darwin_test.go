package cli

import (
	"os/exec"
	"testing"
)

func TestBareRootShowsHelpOnTerminalWithoutInitializing(t *testing.T) {
	bin := buildExecutable(t)
	script := `import os,pty,select,subprocess,sys,tempfile,termios,time
with tempfile.TemporaryDirectory(prefix='recall-help-') as home:
    master,slave=pty.openpty()
    before=termios.tcgetattr(slave)
    try:
        root=os.path.join(home,'missing')
        process=subprocess.Popen([sys.argv[1]],stdin=slave,stdout=slave,stderr=subprocess.PIPE,env=dict(os.environ,HOME=home,RECALL_HOME=root))
        # Drain the terminal while the child runs: help can exceed the pty buffer.
        output=b''
        deadline=time.time()+5
        while process.poll() is None:
            if time.time()>deadline:
                process.kill()
                raise AssertionError('bare root did not exit')
            if select.select([master],[],[],0.05)[0]:
                output+=os.read(master,65536)
        stderr=process.stderr.read()
        while select.select([master],[],[],0)[0]:
            output+=os.read(master,65536)
        assert process.returncode==0,(process.returncode,output,stderr)
        assert b'Usage:' in output and b'Available Commands:' in output,output
        assert not stderr,stderr
        assert b'\x1b' not in output,'bare root entered terminal-control mode'
        assert termios.tcgetattr(slave)==before,'bare root changed terminal settings'
        assert not os.path.exists(root),'help initialized a collection'
    finally:
        os.close(master)
        os.close(slave)
`
	if output, err := exec.CommandContext(t.Context(), "python3", "-c", script, bin).CombinedOutput(); err != nil {
		t.Fatalf("terminal root: %v\n%s", err, output)
	}
}
