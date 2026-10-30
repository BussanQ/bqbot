//go:build bqbot_smoke

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
)

// This opt-in build drives the real app window with an isolated BQBOT_HOME.
func init() {
	serviceListenAddr = "127.0.0.1:0"
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		var win *mygo.Window
		for win == nil {
			select {
			case <-ctx.Done():
				fmt.Fprintln(os.Stderr, "desktop smoke: window timeout")
				os.Exit(1)
			case <-ticker.C:
				for _, w := range mygo.Windows() {
					win = w
					break
				}
			}
		}
		for {
			ready, err := win.Page().EvalContext(ctx, `Boolean(window.mygo && document.getElementById('settings')?.onclick)`)
			if err == nil && ready == true {
				break
			}
			select {
			case <-ctx.Done():
				fmt.Fprintln(os.Stderr, "desktop smoke: frontend timeout", err)
				os.Exit(1)
			case <-ticker.C:
			}
		}
		script := `(async () => {
    const wait = async predicate => { for (let i=0;i<100;i++){if(predicate()) return; await new Promise(r=>setTimeout(r,50));} throw Error('UI timeout'); };
    const click = selector => { const el=document.querySelector(selector); if(!el)throw Error('Missing '+selector);el.click(); };
    await mygo.call('Desktop.Action',{kind:'save-bot',bot:{id:'smoke-bot',name:'演示伙伴',engine:'echo',approval_mode:'ask'}});
    await wait(()=>document.querySelector('[data-select="smoke-bot"]'));
    click('[data-select="smoke-bot"]');
    const draft=document.getElementById('draft');draft.value='你好，验证剩余阶段的桌面功能。';draft.dispatchEvent(new Event('input'));click('#send');
    await wait(()=>document.querySelectorAll('#messages .message').length>=2);
    const titles=[];
    for(const selector of ['[data-tools-home]','[data-voice-home]','[data-team-home]','[data-remote-home]','[data-computer-home]']){
      click('#settings');await wait(()=>document.querySelector(selector));click(selector);
      await wait(()=>document.getElementById('dialog-title').textContent!=='引擎设置');
      titles.push(document.getElementById('dialog-title').textContent);click('#close-dialog');
    }
    await mygo.call('Desktop.Voice',{kind:'save',config:{enabled:true,provider:'say',language:'zh-CN'}});
    click('#settings');await wait(()=>document.querySelector('[data-voice-home]'));click('[data-voice-home]');
    await wait(()=>document.querySelector('[data-voice="read"]'));
    click('#close-dialog');click('[data-speak-message]');
    await new Promise(r=>setTimeout(r,200));
    let audioComplete=false;
    for(let i=0;i<160;i++){
      const audio=await mygo.call('Desktop.Voice',{kind:'status'});
      if(audio.error)throw Error(audio.error);
      if(audio.state==='idle'){audioComplete=true;break;}
      await new Promise(r=>setTimeout(r,100));
    }
    if(!audioComplete)throw Error('audio did not finish');
    click('#settings');await wait(()=>document.querySelector('[data-computer-home]'));click('[data-computer-home]');
    await wait(()=>document.querySelector('[data-host]'));click('[data-host]');
    await wait(()=>document.querySelector('[data-host-action="refresh"]'));
    const host=await mygo.call('Desktop.Computer',{kind:'host-status'});
    if(!host.available || host.version!=='0.28.2')throw Error('bundled host unavailable: '+JSON.stringify(host));
    if(!host.permissions.accessibility || !host.permissions.screen_recording){
      let blocked=false;try{await mygo.call('Desktop.Computer',{kind:'host-start'});}catch{blocked=true;}
      if(!blocked)throw Error('host started without permissions');
    }
    await mygo.call('Desktop.Computer',{kind:'host-stop'});
    click('#close-dialog');
    return {titles,audioComplete,host};
  })()`
		result, err := win.Page().EvalContext(ctx, script)
		if err != nil {
			fmt.Fprintln(os.Stderr, "desktop smoke:", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, "desktop smoke dialogs:", result)
		out := os.Getenv("BQBOT_SMOKE_OUTPUT")
		if out == "" {
			out = "build/smoke"
		}
		_ = os.MkdirAll(out, 0o700)
		for _, theme := range []string{"light", "dark"} {
			_, err = win.Page().EvalContext(ctx, `document.documentElement.dataset.theme="`+theme+`";document.documentElement.style.colorScheme="`+theme+`";true`)
			if err == nil {
				var png []byte
				png, err = win.CapturePage()
				if err == nil {
					err = os.WriteFile(filepath.Join(out, theme+".png"), png, 0o600)
				}
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "desktop smoke screenshot:", err)
				os.Exit(1)
			}
		}
		win.SetSize(720, 600)
		_, err = win.Page().EvalContext(ctx, `document.getElementById('settings').click();(async()=>{for(let i=0;i<100;i++){if(document.getElementById('dialog').open && document.querySelector('[data-computer-home]')){await new Promise(r=>setTimeout(r,100));return true;}await new Promise(r=>setTimeout(r,50));}throw Error('settings did not open');})()`)
		if err == nil {
			var png []byte
			png, err = win.CapturePage()
			if err == nil {
				err = os.WriteFile(filepath.Join(out, "settings-narrow.png"), png, 0o600)
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "desktop smoke:", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, "desktop smoke: passed")
		mygo.App.Quit()
	}()
}
