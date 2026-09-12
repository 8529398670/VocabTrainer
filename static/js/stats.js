// The progress screen: counts, a streak, and a bar per day.
const Stats = {
  // How many days the chart covers. Wide enough to show a habit forming,
  // narrow enough that the bars stay readable on a phone.
  WINDOW_DAYS: 42,

  async init() {
    await Shell.loadSettings();
    this.bindReset();
    await this.load();
  },

  async load() {
    const payload = await Api.stats();
    this.renderTiles( payload );
    this.renderChart( payload.days || [] );
  },

  renderTiles( payload ) {
    const grid = Dom.get( "stat-grid" );
    if ( !grid ) return;
    Dom.clear( grid );

    const counts = payload.counts || {};
    const today = payload.today || {};
    const reviewedToday = ( today.known || 0 ) + ( today.unknown || 0 ) + ( today.skipped || 0 );

    const tiles = [
      { value: payload.streak || 0 , key: "stats.streak_label" },
      { value: reviewedToday , key: "stats.today_label" },
      { value: counts.known || 0 , key: "stats.known_label" },
      { value: counts.unknown || 0 , key: "stats.unknown_label" },
      { value: counts.skipped || 0 , key: "stats.skipped_label" },
      { value: counts.due || 0 , key: "stats.due_label" },
      { value: counts.total || 0 , key: "stats.total_label" },
      { value: Shell.levelName( payload.level ) , key: "stats.level_label" },
    ];

    tiles.forEach( function ( tile ) {
      const label = I18n.get( tile.key );
      if ( label === "" ) return;
      grid.appendChild( Dom.el( "div" , { class: "stat" , children: [
        Dom.el( "div" , { class: "stat-value" , text: tile.value } ),
        Dom.el( "div" , { class: "stat-label" , text: label } ),
      ] } ) );
    } );
  },

  // The server only stores days with activity, so the gaps are filled in
  // here. A chart that silently closed up the empty days would make a broken
  // streak look like an unbroken one.
  renderChart( days ) {
    const chart = Dom.get( "history-chart" );
    const empty = Dom.get( "history-empty" );
    if ( !chart ) return;
    Dom.clear( chart );

    const byDate = {};
    let peak = 0;
    days.forEach( function ( day ) {
      const total = ( day.known || 0 ) + ( day.unknown || 0 ) + ( day.skipped || 0 );
      byDate[ day.date ] = total;
      if ( total > peak ) peak = total;
    } );

    if ( peak === 0 ) {
      Dom.show( chart , false );
      Dom.show( empty , true );
      return;
    }
    Dom.show( chart , true );
    Dom.show( empty , false );

    const pad = function ( value ) { return String( value ).padStart( 2 , "0" ); };
    for ( let back = this.WINDOW_DAYS - 1; back >= 0; back -= 1 ) {
      const when = new Date();
      when.setDate( when.getDate() - back );
      const key = when.getFullYear() + "-" + pad( when.getMonth() + 1 ) + "-" + pad( when.getDate() );
      const total = byDate[ key ] || 0;

      const bar = Dom.el( "div" , { class: "chart-bar" + ( total === 0 ? " is-empty" : "" ) } );
      bar.style.height = total === 0 ? "3px" : Math.max( 4 , Math.round( ( total / peak ) * 100 ) ) + "%";
      bar.setAttribute( "title" , key + ": " + total );
      chart.appendChild( bar );
    }
  },

  bindReset() {
    const form = Dom.get( "reset-form" );
    if ( !form ) return;
    const self = this;
    form.addEventListener( "submit" , async function ( event ) {
      event.preventDefault();
      const field = Dom.get( "reset-confirm" );
      // The server insists on this too. Asking here as well means the
      // mistake is caught before anything is sent.
      if ( !field || field.value.trim() !== "reset" ) return;
      try {
        await Api.resetProgress();
        field.value = "";
        Dom.flash( Dom.get( "reset-done" ) , 3000 );
        await self.load();
      } catch ( error ) {
        Shell.showError( error );
      }
    } );
  },
};

document.addEventListener( "DOMContentLoaded" , function () {
  Shell.boot( "/stats.html" , function () { return Stats.init(); } );
} );
