export function activate(runpilot) {
  runpilot.settings.register({id:"hello", render:()=>runpilot.ui.EmptyState({title:"Hello",message:"A locally installed frontend extension."})});
}
